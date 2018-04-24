package localhelpers

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"io/ioutil"

	"github.com/onsi/ginkgo"
	"github.com/pkg/errors"
)

const defualtEnvoyDockerImage = "soloio/envoy:v0.1.2"

func buildBootstrap(glooAddr string, xdsPort uint32) []byte {
	return []byte(fmt.Sprintf(envoyConfigTemplate, glooAddr, xdsPort))
}

const envoyConfigTemplate = `
node:
 cluster: ingress
 id: testnode

static_resources:
  clusters:
  - name: xds_cluster
    connect_timeout: 5.000s
    hosts:
    - socket_address:
        address: %s
        port_value: %d
    http2_protocol_options: {}
    type: STRICT_DNS

dynamic_resources:
  ads_config:
    api_type: GRPC
    grpc_services:
    - envoy_grpc: {cluster_name: xds_cluster}
  cds_config:
    ads: {}
  lds_config:
    ads: {}
  
admin:
  access_log_path: /dev/null
  address:
    socket_address:
      address: 0.0.0.0
      port_value: 19000

`

type EnvoyFactory struct {
	envoypath string
	tmpdir    string
	useDocker bool
}

func NewEnvoyFactory() (*EnvoyFactory, error) {

	// if an envoy binary is explicitly specified
	// use it
	envoypath := os.Getenv("ENVOY_BINARY")
	if envoypath != "" {
		return &EnvoyFactory{
			envoypath: envoypath,
		}, nil
	}

	switch runtime.GOOS {
	case "darwin":
		return &EnvoyFactory{useDocker: true}, nil
	case "linux":
		// try to grab one form docker...
		tmpdir, err := ioutil.TempDir(os.Getenv("HELPER_TMP"), "envoy")
		if err != nil {
			return nil, err
		}

		envoyImageTag := os.Getenv("ENVOY_IMAGE_TAG")
		if envoyImageTag == "" {
			envoyImageTag = "latest"
		}

		bash := fmt.Sprintf(`
set -ex
CID=$(docker run -d  soloio/envoy:%s /bin/bash -c exit)

# just print the image sha for repoducibility
echo "Using Envoy Image:"
docker inspect soloio/envoy:%s -f "{{.RepoDigests}}"

docker cp $CID:/usr/local/bin/envoy .
docker rm $CID
    `, envoyImageTag, envoyImageTag)
		scriptfile := filepath.Join(tmpdir, "getenvoy.sh")

		ioutil.WriteFile(scriptfile, []byte(bash), 0755)

		cmd := exec.Command("bash", scriptfile)
		cmd.Dir = tmpdir
		cmd.Stdout = ginkgo.GinkgoWriter
		cmd.Stderr = ginkgo.GinkgoWriter
		if err := cmd.Run(); err != nil {
			return nil, err
		}

		return &EnvoyFactory{
			envoypath: filepath.Join(tmpdir, "envoy"),
			tmpdir:    tmpdir,
		}, nil

	default:
		return nil, errors.New("Unsupported OS: " + runtime.GOOS)
	}
}

func (ef *EnvoyFactory) Clean() error {
	if ef == nil {
		return nil
	}
	if ef.tmpdir != "" {
		os.RemoveAll(ef.tmpdir)

	}
	return nil
}

type EnvoyInstance struct {
	envoypath    string
	envoycfgpath string
	tmpdir       string
	cmd          *exec.Cmd
	useDocker    bool
	containerId  string
}

func (ef *EnvoyFactory) NewEnvoyInstance() (*EnvoyInstance, error) {
	var tmpdir string
	var err error
	if ef.useDocker {
		pwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}

		tmpdir = filepath.Join(pwd, "_temp")
		err = os.MkdirAll(tmpdir, 0755)
	} else {
		tmpdir, err = ioutil.TempDir(os.Getenv("HELPER_TMP"), "envoy")
	}
	if err != nil {
		return nil, err
	}

	envoyconfigyaml := filepath.Join(tmpdir, "envoyconfig.yaml")

	return &EnvoyInstance{
		envoypath:    ef.envoypath,
		envoycfgpath: envoyconfigyaml,
		tmpdir:       tmpdir,
		useDocker:    ef.useDocker,
	}, nil

}

func (ei *EnvoyInstance) Run() error {
	return ei.RunWithPort(8081)
}

func (ei *EnvoyInstance) RunWithPort(port uint32) error {
	gloo := "localhost"
	if ei.useDocker {
		var err error
		gloo, err = glooAddr()
		if err != nil {
			return err
		}
	}
	err := ioutil.WriteFile(ei.envoycfgpath, buildBootstrap(gloo, port), 0644)
	if err != nil {
		return err
	}

	if ei.useDocker {
		containerId, err := runContainer(ei.envoycfgpath, port)
		if err != nil {
			return err
		}
		ei.containerId = containerId
		return nil
	}

	// run directly
	cmd := exec.Command(ei.envoypath, "-c", ei.envoycfgpath, "--v2-config-only")
	cmd.Dir = ei.tmpdir
	cmd.Stdout = ginkgo.GinkgoWriter
	cmd.Stderr = ginkgo.GinkgoWriter

	runner := Runner{Sourcepath: ei.envoypath, ComponentName: "ENVOY"}
	cmd, err = runner.run(cmd)
	if err != nil {
		return err
	}
	ei.cmd = cmd
	return nil
}

func (ei *EnvoyInstance) Binary() string {
	return ei.envoypath
}

func (ei *EnvoyInstance) Clean() error {
	if ei.cmd != nil {
		ei.cmd.Process.Kill()
		ei.cmd.Wait()
	}
	if ei.tmpdir != "" {
		os.RemoveAll(ei.tmpdir)
	}
	if ei.containerId != "" {
		if err := stopContainer(ei.containerId); err != nil {
			return err
		}
	}
	return nil
}

func runContainer(cfgpath string, port uint32) (string, error) {
	envoyImageTag := os.Getenv("ENVOY_IMAGE_TAG")
	if envoyImageTag == "" {
		envoyImageTag = "latest"
	}

	cfgDir := filepath.Dir(cfgpath)
	image := "soloio/envoy:" + envoyImageTag
	args := []string{"run", "-d", "--rm",
		"-v", cfgDir + ":/etc/config/",
		"-p", "8080:8080",
		"-p", "19000:19000",
		image,
		"/usr/local/bin/envoy", "--v2-config-only",
		"-c", "/etc/config/" + filepath.Base(cfgpath),
	}
	fmt.Fprintln(ginkgo.GinkgoWriter, args)
	cmd := exec.Command("docker", args...)
	cmd.Dir = cfgDir
	cmd.Stderr = ginkgo.GinkgoWriter
	buffer := &bytes.Buffer{}
	cmd.Stdout = buffer
	err := cmd.Run()
	if err != nil {
		return "", errors.Wrap(err, "Unable to start envoy container")
	}
	return string(buffer.Bytes()), nil

}

func stopContainer(containerId string) error {
	cmd := exec.Command("docker", "stop", containerId)
	cmd.Stdout = ginkgo.GinkgoWriter
	cmd.Stderr = ginkgo.GinkgoWriter
	err := cmd.Run()
	if err != nil {
		return errors.Wrap(err, "Error stopping container "+containerId)
	}
	return nil
}

func glooAddr() (string, error) {
	ip := os.Getenv("GLOO_IP")
	if ip != "" {
		return ip, nil
	}
	// go over network interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, i := range ifaces {
		if (i.Flags&net.FlagUp == 0) ||
			(i.Flags&net.FlagLoopback != 0) ||
			(i.Flags&net.FlagPointToPoint != 0) {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				if v.IP.To4() != nil {
					return v.IP.String(), nil
				}
			case *net.IPAddr:
				if v.IP.To4() != nil {
					return v.IP.String(), nil
				}
			}
		}
	}
	return "", errors.New("unable to find Gloo IP")
}

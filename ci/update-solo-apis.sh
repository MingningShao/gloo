#!/bin/bash

cd ../
git clone https://solo-io:$GITHUB_TOKEN@github.com/solo-io/solo-apis.git
cd solo-apis
git config --global user.email "bot@soloio.com"
git config --global user.name "Gloo Github Action"
#./hack/sync-gloo-apis.sh; make update-deps generate -B
touch test-file
git checkout -b update-solo-apis
git add .
git commit -m "update to latest gloo version" -q --allow-empty
git push --set-upstream origin update-solo-apis
git commit --amend --reset-author
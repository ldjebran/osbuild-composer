#!/bin/bash

set -euo pipefail

# Colorful output.
function greenprint {
  echo -e "\033[1;32m[$(date -Isecond)] ${1}\033[0m"
}

greenprint "📦 Installing epel"
sudo dnf install -y epel-release

greenprint "📦 Installing hashicorp repo"
sudo curl --location --output /etc/yum.repos.d/hashicorp.repo https://rpm.releases.hashicorp.com/RHEL/hashicorp.repo

greenprint "📦 Installing packer and ansible"
sudo dnf install -y packer ansible

greenprint "🖼️ Building an image"

export PKR_VAR_aws_access_key="$V2_AWS_ACCESS_KEY_ID"
export PKR_VAR_aws_secret_key="$V2_AWS_SECRET_ACCESS_KEY"
export PKR_VAR_image_name="osbuild-composer-worker-$CI_COMMIT_SHA"
export PKR_VAR_composer_commit="$CI_COMMIT_SHA"

packer build templates/packer

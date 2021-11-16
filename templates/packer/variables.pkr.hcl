##############################################################################
## VARIABLES THAT ARE SAFE TO CHANGE 🤔

# Git SHA for the osbuild commit to include in the image.
variable "osbuild_commit" {
  type    = string
  default = "f7bf23fabaae6027b1e1147b27870d90d4b1911f"
}

##############################################################################
## VARIABLES THAT PROBABLY SHOULD NOT BE CHANGED 😬

# AWS account configuration.
variable "aws_access_key" { type = string }
variable "aws_secret_key" { type = string }
variable "region" {
  type    = string
  default = "us-east-1"
}

# Automatically set by environment variables in GitHub Actions.
variable "composer_commit" { type = string }
variable "image_name" { type = string }

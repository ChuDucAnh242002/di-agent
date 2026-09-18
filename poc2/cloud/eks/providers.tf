terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.65.0"
    }
  }

  # Left empty here on purpose: CI supplies the bucket/key/region via
  # `terraform init -backend-config=...` (see .github/workflows/cd-eks.yml) so
  # state isn't left behind on an ephemeral runner. For local/manual use, pass
  # the same -backend-config flags yourself, or drop this block to fall back
  # to local state.
  backend "s3" {}
}

provider "aws" {
  region = var.region
}

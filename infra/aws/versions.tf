terraform {
  required_version = ">= 1.8"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Configure a remote backend (e.g. S3) before the first real apply.
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      project = var.name
      managed = "opentofu"
    }
  }
}

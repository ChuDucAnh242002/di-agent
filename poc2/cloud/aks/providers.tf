terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.6.0"
    }
  }

  # Left empty here on purpose: CI supplies the storage account/container via
  # `terraform init -backend-config=...` (see .github/workflows/cd-aks.yml) so
  # state isn't left behind on an ephemeral runner. For local/manual use, pass
  # the same -backend-config flags yourself, or drop this block to fall back
  # to local state.
  backend "azurerm" {}
}

# GitHub Actions enables OIDC through ARM_USE_OIDC=true; leaving this unset
# here also keeps local `az login` authentication working.
provider "azurerm" {
  features {}
}

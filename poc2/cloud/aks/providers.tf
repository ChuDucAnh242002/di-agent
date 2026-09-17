terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 3.100"
    }
  }

  # Left empty here on purpose: CI supplies the storage account/container via
  # `terraform init -backend-config=...` (see .github/workflows/cd-aks.yml) so
  # state isn't left behind on an ephemeral runner. For local/manual use, pass
  # the same -backend-config flags yourself, or drop this block to fall back
  # to local state.
  backend "azurerm" {}
}

# use_oidc lets this run under GitHub Actions' federated credentials
# (azure/login with no client secret) as well as local `az login` sessions.
provider "azurerm" {
  features {}
  use_oidc = true
}

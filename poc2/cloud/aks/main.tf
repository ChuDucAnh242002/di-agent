# AKS cluster backend for poc2 — an alternative to the local libvirt/kubeadm
# lab (see ../../main.tf) for running the di-agent-system Helm chart and the
# di-agent DaemonSet on a managed Azure Kubernetes cluster.

resource "azurerm_resource_group" "this" {
  name     = var.resource_group_name
  location = var.location
}

resource "azurerm_kubernetes_cluster" "this" {
  name                = var.cluster_name
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  dns_prefix          = var.cluster_name
  kubernetes_version  = var.kubernetes_version

  default_node_pool {
    name       = "default"
    node_count = var.node_count
    vm_size    = var.vm_size
  }

  node_provisioning_profile {
    mode = "Manual"
  }

  # SystemAssigned identity avoids managing a service-principal secret.
  identity {
    type = "SystemAssigned"
  }

  network_profile {
    network_plugin = "kubenet"
  }
}

# Optional: only created when var.acr_name is set. Grants the cluster's
# kubelet identity AcrPull so images can be pulled without imagePullSecrets.
resource "azurerm_container_registry" "this" {
  count               = var.acr_name != "" ? 1 : 0
  name                = var.acr_name
  resource_group_name = azurerm_resource_group.this.name
  location            = azurerm_resource_group.this.location
  sku                 = "Basic"
  admin_enabled       = false
}

resource "azurerm_role_assignment" "aks_acr_pull" {
  count                = var.acr_name != "" ? 1 : 0
  scope                = azurerm_container_registry.this[0].id
  role_definition_name = "AcrPull"
  principal_id         = azurerm_kubernetes_cluster.this.kubelet_identity[0].object_id
}

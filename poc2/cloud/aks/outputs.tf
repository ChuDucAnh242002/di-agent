output "resource_group_name" {
  value = azurerm_resource_group.this.name
}

output "cluster_name" {
  value = azurerm_kubernetes_cluster.this.name
}

output "acr_login_server" {
  value = var.acr_name != "" ? azurerm_container_registry.this[0].login_server : ""
}

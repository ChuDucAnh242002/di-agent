# Office-hours schedule: scale the managed node group to zero in the evening
# and back up in the morning. The EKS control plane keeps running (and billing);
# only the EC2 nodes are switched off. Runs outside the cluster (EventBridge
# Scheduler calling the EKS API) so it still works when no nodes exist.

locals {
  nodegroup_name = split(":", module.eks.eks_managed_node_groups["default"].node_group_id)[1]
  nodegroup_arn  = module.eks.eks_managed_node_groups["default"].node_group_arn

  schedule_target_arn = "arn:aws:scheduler:::aws-sdk:eks:updateNodegroupConfig"
}

resource "aws_iam_role" "scheduler" {
  count = var.schedule_enabled ? 1 : 0
  name  = "${var.cluster_name}-nodegroup-scheduler"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "scheduler.amazonaws.com" }
    }]
  })
}

resource "aws_iam_role_policy" "scheduler" {
  count = var.schedule_enabled ? 1 : 0
  name  = "update-nodegroup-config"
  role  = aws_iam_role.scheduler[0].id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["eks:UpdateNodegroupConfig"]
      Resource = [local.nodegroup_arn, module.eks.cluster_arn]
    }]
  })
}

resource "aws_scheduler_schedule" "nodes_up" {
  count                        = var.schedule_enabled ? 1 : 0
  name                         = "${var.cluster_name}-nodes-up"
  schedule_expression          = var.schedule_start
  schedule_expression_timezone = var.schedule_timezone

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = local.schedule_target_arn
    role_arn = aws_iam_role.scheduler[0].arn
    input = jsonencode({
      ClusterName   = var.cluster_name
      NodegroupName = local.nodegroup_name
      ScalingConfig = {
        MinSize     = 0
        MaxSize     = var.node_count + 2
        DesiredSize = var.node_count
      }
    })
  }
}

resource "aws_scheduler_schedule" "nodes_down" {
  count                        = var.schedule_enabled ? 1 : 0
  name                         = "${var.cluster_name}-nodes-down"
  schedule_expression          = var.schedule_stop
  schedule_expression_timezone = var.schedule_timezone

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = local.schedule_target_arn
    role_arn = aws_iam_role.scheduler[0].arn
    input = jsonencode({
      ClusterName   = var.cluster_name
      NodegroupName = local.nodegroup_name
      ScalingConfig = {
        MinSize     = 0
        MaxSize     = var.node_count + 2
        DesiredSize = 0
      }
    })
  }
}

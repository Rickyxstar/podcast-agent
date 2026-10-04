resource "helm_release" "keda" {
  name             = "keda"
  repository       = "https://kedacore.github.io/charts"
  chart            = "keda"
  version          = var.keda_chart_version
  namespace        = "keda"
  create_namespace = true

  # Pods only receive Pod Identity credentials if the association exists
  # when they start.
  depends_on = [
    aws_eks_pod_identity_association.keda,
    module.eks,
  ]
}

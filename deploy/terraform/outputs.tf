output "sut_name" {
  value = google_compute_instance.sut.name
}

output "sut_internal_ip" {
  value = google_compute_instance.sut.network_interface[0].network_ip
}

output "loadgen_name" {
  value = google_compute_instance.loadgen.name
}

output "loadgen_internal_ip" {
  value = google_compute_instance.loadgen.network_interface[0].network_ip
}

output "zone" {
  value = var.zone
}

output "service_account" {
  value = local.sa_email
}

output "ssh" {
  value = <<-EOT
    gcloud compute ssh ${google_compute_instance.sut.name} --zone ${var.zone} --tunnel-through-iap
    gcloud compute ssh ${google_compute_instance.loadgen.name} --zone ${var.zone} --tunnel-through-iap
    Grafana: gcloud compute start-iap-tunnel ${google_compute_instance.sut.name} 3000 --local-host-port=localhost:3000 --zone ${var.zone}
  EOT
}

output "app_public_ip" {
  description = "Для SSH на машину с приложением"
  value       = aws_instance.app.public_ip
}

output "app_private_ip" {
  description = "Адрес приложения для k6, внутри VPC"
  value       = aws_instance.app.private_ip
}

output "loadgen_public_ip" {
  description = "Для SSH на машину с k6"
  value       = aws_instance.loadgen.public_ip
}

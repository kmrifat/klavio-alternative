variable "region" {
  description = "AWS region for SES, SQS and EventBridge."
  type        = string
  default     = "us-east-1"
}

variable "name" {
  description = "Prefix for every resource name."
  type        = string
  default     = "mail"
}

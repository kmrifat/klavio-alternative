output "ses_events_queue_url" {
  description = "Set as SES_EVENTS_QUEUE_URL for the worker."
  value       = aws_sqs_queue.ses_events.url
}

output "ses_events_dlq_url" {
  value = aws_sqs_queue.ses_events_dlq.url
}

output "app_policy_arn" {
  description = "Attach to the role or user the app runs as."
  value       = aws_iam_policy.app.arn
}

output "account_id" {
  value = data.aws_caller_identity.current.account_id
}

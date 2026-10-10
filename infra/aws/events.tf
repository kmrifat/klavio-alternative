# SES publishes sending events (delivery, bounce, complaint, click, ...) to the
# default EventBridge bus. Each brand's configuration set gets an EventBridge
# event destination in M1; this rule forwards all of them to one SQS queue
# that the Go worker long-polls.

resource "aws_sqs_queue" "ses_events_dlq" {
  name                      = "${var.name}-ses-events-dlq"
  message_retention_seconds = 1209600 # 14 days
}

resource "aws_sqs_queue" "ses_events" {
  name                       = "${var.name}-ses-events"
  visibility_timeout_seconds = 60
  message_retention_seconds  = 345600 # 4 days
  receive_wait_time_seconds  = 20     # long polling

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.ses_events_dlq.arn
    maxReceiveCount     = 5
  })
}

resource "aws_cloudwatch_event_rule" "ses_events" {
  name        = "${var.name}-ses-events"
  description = "All Amazon SES sending events"

  event_pattern = jsonencode({
    source = ["aws.ses"]
  })
}

resource "aws_cloudwatch_event_target" "ses_events" {
  rule = aws_cloudwatch_event_rule.ses_events.name
  arn  = aws_sqs_queue.ses_events.arn
}

data "aws_iam_policy_document" "ses_events_queue" {
  statement {
    sid       = "AllowEventBridge"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.ses_events.arn]

    principals {
      type        = "Service"
      identifiers = ["events.amazonaws.com"]
    }

    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [aws_cloudwatch_event_rule.ses_events.arn]
    }
  }
}

resource "aws_sqs_queue_policy" "ses_events" {
  queue_url = aws_sqs_queue.ses_events.id
  policy    = data.aws_iam_policy_document.ses_events_queue.json
}

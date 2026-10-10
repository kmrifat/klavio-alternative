# What the app is allowed to do. Attach this policy to the role the container
# runs as (ECS task role, or an IAM user's keys on a VPS).

data "aws_caller_identity" "current" {}

data "aws_iam_policy_document" "app" {
  statement {
    sid = "SendEmail"

    actions = [
      "ses:SendEmail",
      "ses:SendRawEmail",
    ]
    resources = ["*"] # narrowed to brand identities + configuration sets in M1
  }

  statement {
    sid = "ManageSendingSetup"

    actions = [
      "ses:CreateEmailIdentity",
      "ses:GetEmailIdentity",
      "ses:PutEmailIdentityMailFromAttributes",
      "ses:CreateConfigurationSet",
      "ses:GetConfigurationSet",
      "ses:CreateConfigurationSetEventDestination",
      "ses:GetAccount",
    ]
    resources = ["*"]
  }

  statement {
    sid = "ReadSesEvents"

    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:ChangeMessageVisibility",
      "sqs:GetQueueAttributes",
    ]
    resources = [aws_sqs_queue.ses_events.arn]
  }
}

resource "aws_iam_policy" "app" {
  name   = "${var.name}-app"
  policy = data.aws_iam_policy_document.app.json
}

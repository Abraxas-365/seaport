# Wake agents from CloudWatch alarms

Send CloudWatch alarms to a Seaport webhook dock through EventBridge, and mirror each alarm to a Slack thread where people and the agent talk.

CloudWatch has no webhook of its own, but every alarm state change is an event on Amazon EventBridge, and EventBridge can call an HTTPS endpoint (an **API destination**) with a secret in a header. That is all a [webhook dock](webhooks.md) needs. There is no CloudWatch channel in Seaport.

The result: an alarm goes to `ALARM`, a Slack thread opens in your on-call channel with the alarm, the agent investigates and answers in that thread, and the `OK` event lands in the same thread later.

## Seaport side

1. Create a webhook dock and a secret, give it a team and let its conversations in (see [Webhooks](webhooks.md#create-the-dock)):

   ```sh
   seaport dock add webhook -name cloudwatch
   seaport dock webhook <dock-id> -rotate        # copy the secret
   seaport agents assign <agent> <dock-id> -default
   seaport customs -dock <dock-id> default allow
   ```

2. Save this template. EventBridge delivers the event itself as the body, so the fields are the event's:

   ```json
   {
     "chat_id": "{{.detail.alarmName}}",
     "chat_title": "CloudWatch · {{.detail.alarmName}}",
     "external_id": "{{.id}}",
     "sender_id": "cloudwatch",
     "sender_name": "CloudWatch",
     "text": "**{{.detail.state.value}}** {{.detail.alarmName}} ({{.region}}, account {{.account}})\n{{.detail.state.reason}}\nPrevious: {{.detail.previousState.value}}"
   }
   ```

   Save it as `template.json`, then `seaport dock webhook <dock-id> -template template.json`, or paste it in the dock's page. The `chat_id` is the alarm's name, so every state change of one alarm is one conversation; the `external_id` is the event's id, so EventBridge's retries are one message.

3. Mirror it to Slack. On a [Slack dock](slack.md) that is linked and in the channel, name the place and point the webhook dock at it (see [Mirrors](mirrors.md)):

   ```sh
   seaport dock destination set <slack-dock-id> oncall C0123456789 -title "the on-call channel"
   seaport dock mirror <dock-id> <slack-dock-id>:oncall
   ```

## AWS side

The event comes from the region where the alarm lives, so create these there. Replace `port.example.com` with your public `SEAPORT_PUBLIC_URL` host.

1. A **connection** that sends the secret as `Authorization: Bearer <secret>`:

   ```sh
   aws events create-connection --name seaport \
     --authorization-type API_KEY \
     --auth-parameters '{"ApiKeyAuthParameters":{"ApiKeyName":"Authorization","ApiKeyValue":"Bearer <secret>"}}'
   ```

2. An **API destination** on it, pointing at the dock's URL:

   ```sh
   aws events create-api-destination --name seaport-cloudwatch \
     --connection-arn arn:aws:events:us-east-1:123456789012:connection/seaport/<id> \
     --invocation-endpoint https://port.example.com/hooks/<dock-id> \
     --http-method POST --invocation-rate-limit-per-second 5
   ```

3. A **rule** for alarm events. The pattern below catches every alarm's state change; add `"detail": {"alarmName": [...]}` or `"state": {"value": ["ALARM", "OK"]}` to narrow it:

   ```sh
   aws events put-rule --name seaport-alarms --event-pattern \
     '{"source":["aws.cloudwatch"],"detail-type":["CloudWatch Alarm State Change"]}'
   ```

4. A **role** EventBridge assumes to call the destination (trust `events.amazonaws.com`, permission `events:InvokeApiDestination` on the destination's ARN), and the **target**:

   ```sh
   aws events put-targets --rule seaport-alarms --targets \
     'Id=seaport,Arn=arn:aws:events:us-east-1:123456789012:api-destination/seaport-cloudwatch/<id>,RoleArn=arn:aws:iam::123456789012:role/seaport-events'
   ```

   Leave the target's input as it is: the whole event is sent.

In the console the same steps are **Amazon EventBridge → API destinations** (connection and destination), then **Rules → Create rule** with the AWS service **CloudWatch** event pattern and the API destination as the target.

## Try it

Force an alarm's state without waiting for the metric:

```sh
aws cloudwatch set-alarm-state --alarm-name ServerCpuTooHigh \
  --state-value ALARM --state-reason "Testing the Seaport integration"
aws cloudwatch set-alarm-state --alarm-name ServerCpuTooHigh --state-value OK --state-reason "Done"
```

(`aws events put-events` can't stand in: `aws.*` sources are reserved for AWS.) To test only the dock, post a sample event yourself:

```sh
curl -X POST "https://port.example.com/hooks/<dock-id>" \
  -H "Authorization: Bearer $WEBHOOK_SECRET" -H "Content-Type: application/json" \
  -d '{"version":"0","id":"c4c1c1c9-6542-e61b-6ef0-8c4d36933a92","detail-type":"CloudWatch Alarm State Change","source":"aws.cloudwatch","account":"123456789012","region":"us-east-1","detail":{"alarmName":"ServerCpuTooHigh","state":{"value":"ALARM","reason":"Threshold Crossed"},"previousState":{"value":"OK"}}}'
```

The answer is `202` with `{"accepted":1,"ignored":0}`; the message appears in the dock's conversation, and in Slack when the mirror is set.

## Things to know

- **One thread per alarm.** All events of an alarm share the conversation, so a second `ALARM` days later lands in the same thread and opens no new Slack thread. To start a fresh one, [unlink](linked-conversations.md) the thread.
- **Failed deliveries.** EventBridge retries a failed call for up to 24 hours; the event id makes a retry the same message. A wrong secret is a `404`, indistinguishable from an unknown dock. An API destination waits about 5 s for the answer; when Slack is slow to take the mirror's first post, EventBridge may give up and retry, and the retry is the same message (no second Slack thread).
- **Limits.** The dock takes 60 calls a minute (see [Limits](../reference/limits.md#webhook-docks)); alarms rarely come close, and the destination's rate limit above keeps bursts in check.
- The event shape is AWS's [Alarm events and EventBridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html).

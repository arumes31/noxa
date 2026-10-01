# Incoming webhooks

In a channel, open **Threads and forum → Incoming webhooks**. Channel managers can create a named integration, copy its one-time bearer token, list integrations and revoke one. Creating and using a hook requires the creator's current View Channel, Read History, Manage Channels and Send Messages rights. Removing those rights, deleting the creator/channel, banning the creator, or revoking the hook stops later posts. A hook can post only to its fixed channel.

The server's existing HTTP listener accepts `POST /hooks/<id>`. Publish that path through your HTTPS reverse proxy for remote callers; do not expose bearer credentials over unencrypted networks. The settings dialog displays the internal port and path. Tokens belong in the Authorization header, never the URL.

```sh
curl -X POST 'https://voice.example.com/hooks/123' \
  -H "Authorization: Bearer $NOXA_WEBHOOK_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"content":"Build passed"}'
```

A successful response is HTTP 201 with `{"message_id":123}`. Invalid/revoked credentials return 401; invalid content returns 400; rate limits return 429 with Retry-After; unavailable storage/policy returns 503. The limit is five posts per three seconds per hook, twenty hooks per channel, and 12,000 bytes of content (or the server's smaller chat limit). Slow mode and content moderation also apply. Only the `content` property is supported. There are no outbound requests, remote attachment fetches, arbitrary destinations, or mention notifications.

Only the SHA-256 digest of the 256-bit secret is stored. Post/revoke operations serialize on the hook row; policy and admission checks remain locked through encryption, commit and broadcast. The server receives webhook plaintext like other server-mediated channel chat, then encrypts it with the current channel key before storage and broadcast. Messages are attributed to `Integration name [Webhook]`. Private-message end-to-end encryption is unaffected.

HTTP clients should not automatically retry ambiguous transport failures: an accepted post may already have committed.

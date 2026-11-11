# Link a WhatsApp number

A WhatsApp dock links a number to the port as a *linked device*, the way WhatsApp Web does. People write to that number; Seaport receives their messages, passes them through customs and gives them to your agents; the agents' replies go out from the same number.

## Before you start

- A running port. Use a number you control and can afford to lose: WhatsApp may ban numbers that behave like bots. Seaport limits how fast a dock sends (see [limits](../reference/limits.md)), but it can't make an unofficial client official.
- The phone with that number at hand.

## Link it

In the web app: **Muelles** (Docks) → **Nuevo muelle** → WhatsApp. Or from the CLI:

```sh
seaport dock add whatsapp -name sales
```

Seaport shows a QR code (the CLI draws it in the terminal). On the phone, open WhatsApp → **Linked devices** → **Link a device** and scan it. The dock goes from `linking` to `linked` and shows its number. The code refreshes while you wait; if nobody scans it within about three minutes the dock turns `failed`: **relink** it to get a new code.

```sh
seaport dock list
seaport dock relink <dock-id>      # a new QR for a failed or logged-out dock
seaport dock rename <dock-id> ventas
```

## Try it with your own chat

Your "message yourself" chat with the linked number is always allowed by customs. Write to yourself from the phone and the message reaches the dock's agent: the easiest way to see the whole loop.

## What arrives

| WhatsApp | In Seaport |
| :--- | :--- |
| Text, captions, replies | the message's text |
| Photos | an image attachment (see [Messages and images](messages-and-images.md)) |
| Videos, documents, audio, stickers, locations, contacts | a note in the text for now: the caption as `[video] caption`, or `[audio received; media is not supported yet]` |
| Status updates, channels (newsletters) | ignored |
| Your own messages to other chats | ignored (and the echo of what agents send is dropped) |
| Group messages | kept; in `mentions` mode only those that @mention the number or reply to it wake an agent |

Customs identifies people by their phone as `+<digits>` and by their WhatsApp ids (`…@s.whatsapp.net`, `…@lid`); groups by `…@g.us`. Rules can match any of these: see [Customs](customs.md).

## Unlink or delete

```sh
seaport dock unlink <dock-id>       # log the device out; history stays, relink to pair again
seaport dock delete <dock-id> -yes  # unlink and delete its conversations, messages and customs
```

Unlinking removes the device from the phone's list and deletes its keys from Seaport. You can also remove the device from the phone; the dock turns `logged_out`.

## How the keys are kept

The device's keys live in Postgres, sealed with the port's secret key (AES-GCM, a purpose per table, dock and row, so a value copied elsewhere doesn't open). Losing the secret key means linking every WhatsApp dock again. With several Seaport processes, each WhatsApp dock runs in exactly one of them; see [Several processes](operations/several-processes.md).

## Next

- [Customs](customs.md): decide which chats wake an agent.
- [Agents and routing](agents-and-routing.md): who answers.

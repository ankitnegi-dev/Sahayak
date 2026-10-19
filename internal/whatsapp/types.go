package whatsapp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrMalformedPayload means a webhook body did not decode, or decoded into
// something that cannot be interpreted as a message.
var ErrMalformedPayload = errors.New("malformed payload")

// Message types Meta can deliver on the messages webhook.
const (
	MessageTypeText        = "text"
	MessageTypeAudio       = "audio"
	MessageTypeImage       = "image"
	MessageTypeVideo       = "video"
	MessageTypeDocument    = "document"
	MessageTypeSticker     = "sticker"
	MessageTypeLocation    = "location"
	MessageTypeContacts    = "contacts"
	MessageTypeInteractive = "interactive"
	MessageTypeButton      = "button"
	MessageTypeReaction    = "reaction"
	MessageTypeSystem      = "system"
	MessageTypeUnsupported = "unsupported"
)

// WebhookPayload is the top-level body Meta POSTs to the webhook.
type WebhookPayload struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

// Entry is one business account entry. There is almost always exactly one.
type Entry struct {
	ID      string   `json:"id"`
	Changes []Change `json:"changes"`
}

// Change wraps the changed field and its value.
type Change struct {
	Field string `json:"field"`
	Value Value  `json:"value"`
}

// Value is the payload for the "messages" field.
type Value struct {
	MessagingProduct string    `json:"messaging_product"`
	Metadata         Metadata  `json:"metadata"`
	Contacts         []Contact `json:"contacts"`
	Messages         []Message `json:"messages"`
	// Statuses carries delivery receipts. These are not inbound messages and
	// must not be treated as user input.
	Statuses []Status       `json:"statuses"`
	Errors   []WebhookError `json:"errors"`
}

// Metadata identifies the business phone number that received the message.
type Metadata struct {
	DisplayPhoneNumber string `json:"display_phone_number"`
	PhoneNumberID      string `json:"phone_number_id"`
}

// Contact is the WhatsApp user who sent the message.
type Contact struct {
	Profile Profile `json:"profile"`
	WaID    string  `json:"wa_id"`
}

// Profile is the sender's display name, which parents often set in Hinglish.
type Profile struct {
	Name string `json:"name"`
}

// Message is one inbound message from a parent.
type Message struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`

	Text     *TextContent     `json:"text,omitempty"`
	Audio    *AudioContent    `json:"audio,omitempty"`
	Image    *MediaContent    `json:"image,omitempty"`
	Video    *MediaContent    `json:"video,omitempty"`
	Document *MediaContent    `json:"document,omitempty"`
	Sticker  *MediaContent    `json:"sticker,omitempty"`
	Location *LocationContent `json:"location,omitempty"`
	Context  *Context         `json:"context,omitempty"`
	Errors   []WebhookError   `json:"errors,omitempty"`
	Referral *Referral        `json:"referral,omitempty"`
}

// Context is set when this message replies to an earlier one.
type Context struct {
	From string `json:"from"`
	ID   string `json:"id"`
}

// Referral is set when the parent clicked an ad or link that started the chat.
type Referral struct {
	SourceURL  string `json:"source_url"`
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Headline   string `json:"headline"`
	Body       string `json:"body"`
}

// TextContent holds the body of a text message.
type TextContent struct {
	Body string `json:"body"`
}

// MediaContent is the shape Meta uses for audio, image, video, document and
// sticker. The inner ID is a media ID that must be resolved through the Graph
// API to obtain a download URL. That URL is short lived.
type MediaContent struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	SHA256   string `json:"sha256"`
	Filename string `json:"filename,omitempty"`
	Caption  string `json:"caption,omitempty"`
	Voice    bool   `json:"voice,omitempty"`
}

// AudioContent is media content. It is a distinct named type so that Phase 3
// can express voice-specific intent without reaching into a generic struct.
type AudioContent struct {
	MediaContent
}

// LocationContent is a shared location pin.
type LocationContent struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name"`
	Address   string  `json:"address"`
}

// Status is a delivery receipt, not an inbound message.
type Status struct {
	ID           string        `json:"id"`
	RecipientID  string        `json:"recipient_id"`
	Status       string        `json:"status"`
	Timestamp    string        `json:"timestamp"`
	Conversation *Conversation `json:"conversation,omitempty"`
}

// Conversation identifies the thread a status belongs to.
type Conversation struct {
	ID   string `json:"id"`
	From string `json:"from"`
}

// WebhookError is an error object Meta embeds in the webhook payload itself,
// distinct from an HTTP-level APIError returned by our Graph API client.
type WebhookError struct {
	Code    int            `json:"code"`
	Title   string         `json:"title"`
	Message string         `json:"message"`
	Details map[string]any `json:"error_data,omitempty"`
}

// IncomingMessage is the normalised form of a Message, decoupled from Meta's
// JSON shape. Everything downstream (jobs, conversation engine, ASR) works with
// this type, so a Meta API version bump only touches this package.
type IncomingMessage struct {
	// ID is the WhatsApp message ID, used as the idempotency key.
	ID string
	// From is the parent's WhatsApp ID, in international format without a
	// leading plus, for example "919812345678".
	From string
	// ProfileName is the parent's display name, if Meta sent one.
	ProfileName string
	// Type is one of the MessageType constants, or MessageTypeUnsupported for
	// anything the bot cannot yet handle.
	Type string
	// Text is the body for text messages.
	Text string
	// MediaID is the Meta media ID for audio and other attachments.
	MediaID string
	// MimeType is the media MIME type, for example "audio/ogg; codecs=opus".
	MimeType string
	// Filename is the original filename for documents.
	Filename string
	// Caption is the user-supplied caption on an image or video.
	Caption string
	// Timestamp is when Meta says the message was sent.
	Timestamp time.Time
	// IsForwarded is true when the parent forwarded a message from someone
	// else. Forwarded content is untrusted input and is treated as such by
	// the safety layer.
	IsForwarded bool
	// ReplyToMessageID is set when the parent replied to an earlier message.
	ReplyToMessageID string
}

// Normalize converts a Meta payload into zero or more normalised messages.
//
// A single webhook delivery can contain several messages, and can contain
// delivery statuses instead of messages. Both cases return an empty slice
// without an error, because "nothing to do" is not a failure.
//
// Errors are reserved for payloads that claim to contain a message we cannot
// make sense of, such as one with no ID. Silently dropping those would mean
// losing a parent's message, and the audit trail must show it.
func Normalize(p WebhookPayload) ([]IncomingMessage, error) {
	var out []IncomingMessage

	for _, entry := range p.Entry {
		for _, change := range entry.Changes {
			if change.Field != "messages" {
				continue
			}

			// Meta sends contacts alongside messages, matched by wa_id.
			names := make(map[string]string, len(change.Value.Contacts))
			for _, c := range change.Value.Contacts {
				names[c.WaID] = c.Profile.Name
			}

			for _, m := range change.Value.Messages {
				msg, err := normalizeMessage(m, names)
				if err != nil {
					return nil, err
				}
				out = append(out, msg)
			}
		}
	}

	return out, nil
}

func normalizeMessage(m Message, names map[string]string) (IncomingMessage, error) {
	if strings.TrimSpace(m.ID) == "" {
		return IncomingMessage{}, fmt.Errorf("whatsapp: message has no id: %w", ErrMalformedPayload)
	}

	msg := IncomingMessage{
		ID:               m.ID,
		From:             m.From,
		ProfileName:      names[m.From],
		Type:             normaliseType(m.Type),
		Timestamp:        parseTimestamp(m.Timestamp),
		IsForwarded:      isForwarded(m),
		ReplyToMessageID: replyTo(m),
	}

	switch msg.Type {
	case MessageTypeText:
		if m.Text != nil {
			msg.Text = m.Text.Body
		}
	case MessageTypeAudio:
		if m.Audio != nil {
			msg.MediaID = m.Audio.ID
			msg.MimeType = m.Audio.MimeType
			msg.Caption = m.Audio.Caption
		}
	case MessageTypeImage, MessageTypeVideo, MessageTypeDocument, MessageTypeSticker:
		content := firstMedia(m)
		if content != nil {
			msg.MediaID = content.ID
			msg.MimeType = content.MimeType
			msg.Filename = content.Filename
			msg.Caption = content.Caption
		}
	}

	return msg, nil
}

// normaliseType maps Meta's type string onto our constants, collapsing
// anything unrecognised onto MessageTypeUnsupported.
//
// This is forward compatible on purpose. Meta adds new message types
// regularly, and an unknown type must degrade to "we cannot handle this yet"
// rather than crashing the worker.
func normaliseType(t string) string {
	switch t {
	case MessageTypeText:
		return MessageTypeText
	case MessageTypeAudio:
		return MessageTypeAudio
	case MessageTypeImage:
		return MessageTypeImage
	case MessageTypeVideo:
		return MessageTypeVideo
	case MessageTypeDocument:
		return MessageTypeDocument
	case MessageTypeSticker:
		return MessageTypeSticker
	case MessageTypeLocation:
		return MessageTypeLocation
	case MessageTypeContacts:
		return MessageTypeContacts
	case MessageTypeInteractive:
		return MessageTypeInteractive
	case MessageTypeButton:
		return MessageTypeButton
	case MessageTypeReaction:
		return MessageTypeReaction
	case MessageTypeSystem:
		return MessageTypeSystem
	default:
		return MessageTypeUnsupported
	}
}

func firstMedia(m Message) *MediaContent {
	switch {
	case m.Image != nil:
		return m.Image
	case m.Video != nil:
		return m.Video
	case m.Document != nil:
		return m.Document
	case m.Sticker != nil:
		return m.Sticker
	default:
		return nil
	}
}

// isForwarded reports whether the parent forwarded this message from someone
// else.
//
// Forwarded text is attacker-controlled content from an unknown third party.
// The safety layer treats it as untrusted input, so detecting it early keeps
// the decision in one place.
func isForwarded(m Message) bool {
	// Meta marks forwards with a context whose ID is empty, and the raw
	// payload carries forward flag keys that do not survive into our struct
	// on every API version. A context block with no message ID is the
	// documented signal.
	return m.Context != nil && m.Context.ID == ""
}

func replyTo(m Message) string {
	if m.Context == nil {
		return ""
	}
	return m.Context.ID
}

// parseTimestamp converts Meta's timestamp, which arrives as a string of Unix
// seconds such as "1603059201".
//
// An unparseable value becomes the zero time rather than an error, because a
// bad timestamp must never stop us from replying to a parent. Callers that
// need ordering should fall back to arrival time.
func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0).UTC()
}

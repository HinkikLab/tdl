package texpr

import (
	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
)

type EnvMessage struct {
	Mentioned     bool            `comment:"Whether we were mentioned in this message" comment_id:"fields.whether_we_were_mentioned_in_this_message"`
	Silent        bool            `comment:"Whether this is a silent message (no notification triggered)" comment_id:"fields.whether_this_is_a_silent_message_no_notification_triggered"`
	FromScheduled bool            `comment:"Whether this is a scheduled message" comment_id:"fields.whether_this_is_a_scheduled_message"`
	Pinned        bool            `comment:"Whether this message is pinned" comment_id:"fields.whether_this_message_is_pinned"`
	ID            int             `comment:"ID of the message" comment_id:"fields.id_of_the_message"`
	FromID        int64           `comment:"ID of the sender of the message" comment_id:"fields.id_of_the_sender_of_the_message"`
	Date          int             `comment:"Date of the message" comment_id:"fields.date_of_the_message"`
	Message       string          `comment:"The message" comment_id:"fields.the_message"`
	Media         EnvMessageMedia `comment:"Media attachment" comment_id:"fields.media_attachment"`
	Views         int             `comment:"View count" comment_id:"fields.view_count"`
	Forwards      int             `comment:"Forward count" comment_id:"fields.forward_count"`
}

type EnvMessageMedia struct {
	Name string `comment:"File name" comment_id:"fields.file_name"`
	Size int64  `comment:"File size. Unit: Byte" comment_id:"fields.file_size_unit_byte"`
	DC   int    `comment:"DC ID" comment_id:"fields.dc_id"`
}

func ConvertEnvMessage(msg *tg.Message) EnvMessage {
	m := EnvMessage{}
	if msg == nil {
		return m
	}

	m.Mentioned = msg.Mentioned
	m.Silent = msg.Silent
	m.FromScheduled = msg.FromScheduled
	m.Pinned = msg.Pinned
	m.ID = msg.ID
	m.FromID = tutil.GetPeerID(msg.FromID)
	m.Date = msg.Date
	m.Message = msg.Message

	if media, ok := tmedia.GetMedia(msg); ok {
		m.Media = EnvMessageMedia{
			Name: media.Name,
			Size: media.Size,
			DC:   media.DC,
		}
	}

	m.Views = msg.Views
	m.Forwards = msg.Forwards

	return m
}

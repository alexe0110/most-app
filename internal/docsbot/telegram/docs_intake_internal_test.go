package telegram

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestIncomingFile(t *testing.T) {
	tests := []struct {
		name string
		msg  *models.Message
		want docsbot.IncomingFile
	}{
		{
			name: "photo without caption has no title",
			msg: &models.Message{Photo: []models.PhotoSize{
				{FileID: "small", FileUniqueID: "us", FileSize: 10},
				{FileID: "big", FileUniqueID: "ub", FileSize: 2048},
			}},
			want: docsbot.IncomingFile{UserID: "u", Kind: docsbot.KindPhoto, FileID: "big", FileUniqueID: "ub", FileSize: 2048},
		},
		{
			name: "photo caption becomes title",
			msg:  &models.Message{Caption: "Чек", Photo: []models.PhotoSize{{FileID: "p", FileUniqueID: "up", FileSize: 5}}},
			want: docsbot.IncomingFile{UserID: "u", Kind: docsbot.KindPhoto, FileID: "p", FileUniqueID: "up", Title: "Чек", FileSize: 5},
		},
		{
			name: "document without name or caption has no title",
			msg:  &models.Message{Document: &models.Document{FileID: "d", FileUniqueID: "ud", FileSize: 7, MimeType: "application/pdf"}},
			want: docsbot.IncomingFile{UserID: "u", Kind: docsbot.KindDocument, FileID: "d", FileUniqueID: "ud", FileSize: 7, MIME: "application/pdf"},
		},
		{
			name: "document file name loses its extension",
			msg: &models.Message{Caption: "подпись", Document: &models.Document{
				FileID: "d", FileUniqueID: "ud", FileName: "scan.pdf", FileSize: 99, MimeType: "application/pdf",
			}},
			want: docsbot.IncomingFile{
				UserID: "u", Kind: docsbot.KindDocument, FileID: "d", FileUniqueID: "ud",
				Title: "scan", FileSize: 99, MIME: "application/pdf",
			},
		},
		{
			name: "document caption used when no file name",
			msg:  &models.Message{Caption: "подпись", Document: &models.Document{FileID: "d", FileUniqueID: "ud"}},
			want: docsbot.IncomingFile{UserID: "u", Kind: docsbot.KindDocument, FileID: "d", FileUniqueID: "ud", Title: "подпись"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := incomingFile(tt.msg, "u")
			require.True(t, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIncomingFile_photoPixelSize(t *testing.T) {
	m := &models.Message{Photo: []models.PhotoSize{
		{FileID: "s", FileUniqueID: "us", Width: 90, Height: 60, FileSize: 1},
		{FileID: "l", FileUniqueID: "ul", Width: 1280, Height: 960, FileSize: 2},
	}}
	in, ok := incomingFile(m, "u")
	require.True(t, ok)
	require.Equal(t, "l", in.FileID)
	require.Equal(t, 1280, in.Width)
	require.Equal(t, 960, in.Height)

	// The Bot API gives a document no pixel size, and the bot does not download to measure.
	in, ok = incomingFile(&models.Message{Document: &models.Document{FileID: "d", FileUniqueID: "ud", MimeType: "image/png"}}, "u")
	require.True(t, ok)
	require.Zero(t, in.Width)
	require.Zero(t, in.Height)
}

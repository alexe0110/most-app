package docsbottest

import (
	"context"
	"errors"
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// FakeDrive reports a configurable connection state and upload outcome. The
// zero value is "not connected", like the production stub. A pointer or slice
// field, when set, records what the code under test did.
type FakeDrive struct {
	IsConnected bool // IsConnected, not Connected: that is the port method
	Err         error
	UploadFile  docsbot.DriveFile
	UploadErr   error
	Uploads     *int  // counts Upload calls when set
	RevokeErr   error // returned by Revoke
	Revoked     *bool // set to true on Revoke when provided

	DisconnectErr error // returned by Disconnect
	Disconnected  *bool // set to true on Disconnect when provided

	// Cloud sync calls are appended to Calls as "op:remoteID[:arg]" when set; the
	// matching *Err is returned by that op.
	Calls      *[]string
	RenameErr  error
	MoveErr    error
	TrashErr   error
	ReplaceErr error
	UploadedAs *string // last Upload name, when set

	// FileExists: ExistsRes is the answer (false, "the file is gone", unless set),
	// ExistsErr the failure; ExistsCalls counts calls and CheckDeadline records the
	// context's remaining time on each call.
	ExistsRes     bool
	ExistsErr     error
	ExistsCalls   *int
	CheckDeadline *time.Duration
}

func (d FakeDrive) record(op string) {
	if d.Calls != nil {
		*d.Calls = append(*d.Calls, op)
	}
}

func (d FakeDrive) Rename(_ context.Context, _ docsbot.UserID, remoteID, name string) error {
	d.record("rename:" + remoteID + ":" + name)
	return d.RenameErr
}

func (d FakeDrive) Move(_ context.Context, _ docsbot.UserID, remoteID string, cat docsbot.Category) error {
	d.record("move:" + remoteID + ":" + string(cat))
	return d.MoveErr
}

func (d FakeDrive) Trash(_ context.Context, _ docsbot.UserID, remoteID string) error {
	d.record("trash:" + remoteID)
	return d.TrashErr
}

func (d FakeDrive) FileExists(ctx context.Context, _ docsbot.UserID, remoteID string) (bool, error) {
	d.record("exists:" + remoteID)
	if d.ExistsCalls != nil {
		*d.ExistsCalls++
	}
	if dl, ok := ctx.Deadline(); ok && d.CheckDeadline != nil {
		*d.CheckDeadline = time.Until(dl)
	}
	return d.ExistsRes, d.ExistsErr
}

func (d FakeDrive) Replace(_ context.Context, _ docsbot.UserID, remoteID, _ string, _ []byte) error {
	d.record("replace:" + remoteID)
	return d.ReplaceErr
}

func (d FakeDrive) Connected(context.Context, docsbot.UserID) (bool, error) {
	return d.IsConnected, d.Err
}

func (d FakeDrive) Upload(_ context.Context, _ docsbot.UserID, in docsbot.DriveUpload) (docsbot.DriveFile, error) {
	if d.Uploads != nil {
		*d.Uploads++
	}
	if d.UploadedAs != nil {
		*d.UploadedAs = in.Name
	}
	return d.UploadFile, d.UploadErr
}

func (d FakeDrive) Revoke(context.Context, docsbot.UserID) error {
	if d.Revoked != nil {
		*d.Revoked = true
	}
	return d.RevokeErr
}

func (d FakeDrive) Disconnect(context.Context, docsbot.UserID) error {
	if d.Disconnected != nil {
		*d.Disconnected = true
	}
	return d.DisconnectErr
}

// FakeFiles returns a configurable download result.
type FakeFiles struct {
	Data      []byte
	MIME      string
	Err       error
	Downloads *int // counts Download calls when set
}

func (f FakeFiles) Download(context.Context, string) ([]byte, string, error) {
	if f.Downloads != nil {
		*f.Downloads++
	}
	return f.Data, f.MIME, f.Err
}

// FakeAuth returns a fixed consent URL for the connect button.
type FakeAuth struct{}

func (FakeAuth) AuthURL(context.Context, string, string) (string, error) {
	return "https://auth.example/consent", nil
}

// NoFiles is a file source for tests that never download.
type NoFiles struct{}

func (NoFiles) Download(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("unexpected download")
}

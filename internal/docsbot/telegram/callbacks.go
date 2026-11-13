package telegram

import (
	"fmt"
	"strings"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// cbPrefix tags every inline button the docs feature owns, keeping it clear of
// the /convert buttons ("conv:").
const cbPrefix = "d:"

// Callback actions. Data is "d:<action>" or "d:<action>:<docID>[:<arg>]".
const (
	actCategory      = "cat" // pick a category for a new document
	actCategoryGroup = "ga"  // pick a category for a whole album: d:ga:<docID>:<slug>
	actGroupPick     = "gpk" // album choice -> category picker: d:gpk:<docID>
	actSetCategory   = "sc"  // change an existing document's category
	actChangeCat     = "chc" // show the category picker for a card
	actSend          = "snd" // resend the original file
	actRename        = "rn"  // ask for a new title: the card message becomes the prompt
	actRenameCancel  = "rnc" // cancel the rename prompt: d:rnc:<docID>
	actDeleteAsk     = "del" // ask to confirm deletion
	actDeleteYes     = "dy"  // confirm deletion
	actDeleteNo      = "dn"  // cancel deletion
	actListCat       = "lc"  // open a category page: d:lc:<slug>:<page>
	actListBack      = "lb"  // back to the category list
	actTransfer      = "tr"  // transfer saved documents to Drive
	actTransferYes   = "try" // confirm the transfer offer
	actTransferNo    = "trn" // decline the transfer offer
	actRetry         = "rt"  // retry a failed backup: d:rt:<docID>
	actRetrySync     = "rs"  // retry a failed cloud sync: d:rs:<docID>
	actDisconnect    = "dc"  // disconnect Google Drive
	actRetryAll      = "rta" // retry every failed backup
	actExport        = "exp" // resend all documents from Telegram
	actDeleteData    = "dd"  // start the delete-all-data confirmation
	actRetryBatch    = "rsm" // retry failed docs in a status message: d:rsm:<smID>
	actOpenCard      = "oc"  // open a card: d:oc:<docID>:<l|s>[:<cat>:<page>]
	actSearchBack    = "sb"  // re-run the last search
	actInline        = "inl" // switch inline mode: d:inl:<1|0> is the wanted state
	actNoop          = "noop"
)

// cb builds "d:<action>:<docID>".
func cb(action string, id docsbot.DocumentID) string {
	return fmt.Sprintf("%s%s:%s", cbPrefix, action, id)
}

// backFromOrig is the callback of the card's "Назад" button for an origin token:
// the category page for "l:<cat>:<page>", the last search for "s", "" otherwise.
func backFromOrig(orig string) string {
	switch {
	case strings.HasPrefix(orig, "l:"):
		return cbPrefix + actListCat + strings.TrimPrefix(orig, "l")
	case orig == "s":
		return cbPrefix + actSearchBack
	}
	return ""
}

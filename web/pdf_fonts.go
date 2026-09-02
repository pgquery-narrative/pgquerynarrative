package web

import (
	_ "embed"

	"github.com/jung-kurt/gofpdf/v2"
)

// The Go fonts (Bigelow & Holmes, BSD-style license — see web/fonts/LICENSE)
// give the PDF export a real Unicode text path: Latin, Latin Extended, Greek,
// Cyrillic, and the punctuation an LLM narrative actually produces (en/em
// dashes, curly quotes, ellipsis, bullets, arrows). This replaces gofpdf's
// built-in WinAnsi/Latin-1 core fonts, which forced every other rune through a
// "?" substitution.

//go:embed fonts/Go-Regular.ttf
var goFontRegular []byte

//go:embed fonts/Go-Bold.ttf
var goFontBold []byte

//go:embed fonts/Go-Mono.ttf
var goFontMono []byte

// Font family names registered on every PDF. pdfFontBody is the proportional
// text face (regular + bold); pdfFontMono is for SQL and plan snippets.
const (
	pdfFontBody = "body"
	pdfFontMono = "mono"
)

// registerPDFFonts installs the embedded Unicode fonts on pdf. Call once, right
// after gofpdf.New, before the first SetFont.
func registerPDFFonts(pdf *gofpdf.Fpdf) {
	pdf.AddUTF8FontFromBytes(pdfFontBody, "", goFontRegular)
	pdf.AddUTF8FontFromBytes(pdfFontBody, "B", goFontBold)
	pdf.AddUTF8FontFromBytes(pdfFontMono, "", goFontMono)
}

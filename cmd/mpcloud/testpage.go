package main

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// testPagePDF returns a one-page US Letter PDF (content also fits A4) with a
// border, grayscale ramp, color swatches, line widths and text sizes.
func testPagePDF() []byte {
	const w, h = 612, 792
	var c strings.Builder
	esc := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)
	text := func(x, y, size int, font, s string) {
		fmt.Fprintf(&c, "0 0 0 rg BT /%s %d Tf %d %d Td (%s) Tj ET\n", font, size, x, y, esc.Replace(s))
	}
	box := func(x, y, bw, bh int, r, g, b float64) {
		fmt.Fprintf(&c, "%.2f %.2f %.2f rg 0 0 0 RG 0.5 w %d %d %d %d re B\n", r, g, b, x, y, bw, bh)
	}

	fmt.Fprintf(&c, "0 0 0 RG 1.5 w 36 36 %d %d re S\n", w-72, h-72)
	text(60, 710, 30, "F2", "Printer Test Page")
	text(60, 684, 13, "F1", "Printed from Linux with mpcloud")
	text(60, 666, 11, "F1", "Generated "+time.Now().Format("2006-01-02 15:04"))

	text(60, 630, 12, "F2", "Grayscale")
	for i := 0; i <= 10; i++ {
		g := 1 - float64(i)/10
		box(60+i*42, 580, 42, 40, g, g, g)
		text(60+i*42+12, 566, 8, "F1", fmt.Sprintf("%d%%", i*10))
	}

	text(60, 530, 12, "F2", "Color (prints as gray on a black & white printer)")
	swatches := []struct {
		name    string
		r, g, b float64
	}{{"Cyan", 0, 1, 1}, {"Magenta", 1, 0, 1}, {"Yellow", 1, 1, 0}, {"Red", 1, 0, 0}, {"Green", 0, 0.6, 0}, {"Blue", 0, 0, 1}, {"Black", 0, 0, 0}}
	for i, s := range swatches {
		box(60+i*66, 470, 60, 50, s.r, s.g, s.b)
		text(60+i*66+4, 456, 9, "F1", s.name)
	}

	text(60, 420, 12, "F2", "Line widths")
	for i, lw := range []float64{0.25, 0.5, 1, 2, 4} {
		y := 400 - i*14
		fmt.Fprintf(&c, "0 0 0 RG %.2f w 60 %d m 520 %d l S\n", lw, y, y)
		text(528, y-3, 8, "F1", fmt.Sprintf("%gpt", lw))
	}

	y := 310
	for _, size := range []int{6, 8, 10, 12, 16, 20} {
		s := fmt.Sprintf("%dpt  The quick brown fox jumps over the lazy dog 0123456789", size)
		if n := 900 / size; len(s) > n {
			s = s[:n]
		}
		text(60, y, size, "F1", s)
		y -= size + 8
	}
	text(60, 60, 9, "F1", "The border is 0.5in from the page edges. If a side is cut off, the printer's margins are larger.")

	stream := c.String()
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R /Resources << /Font << /F1 5 0 R /F2 6 0 R >> >> >>", w, h),
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

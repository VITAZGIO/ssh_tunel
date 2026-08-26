package tunnel

import (
	"bufio"
	"bytes"
	"io"
	"testing"
)

// sizeWriter запоминает, какими кусками в него писали.
type sizeWriter struct {
	got   bytes.Buffer
	sizes []int
}

func (w *sizeWriter) Write(p []byte) (int, error) {
	w.sizes = append(w.sizes, len(p))
	return w.got.Write(p)
}

// Ради этого копия и написана вручную: io.CopyBuffer на таких сторонах
// подсовывает свои 32 КБ, и пул буферов оказывается ни при чём.
func TestCopyBufUsesGivenBuffer(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 200*1024)
	// bufio.Reader — ровно тот случай, на котором io.CopyBuffer отдаёт работу
	// чужому WriteTo: так к нам приходит клиент после разбора CONNECT.
	src := bufio.NewReader(bytes.NewReader(data))
	dst := &sizeWriter{}

	buf := make([]byte, bufSize)
	n, err := copyBuf(dst, src, buf)
	if err != nil {
		t.Fatalf("копирование не удалось: %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("скопировано %d байт вместо %d", n, len(data))
	}
	if !bytes.Equal(dst.got.Bytes(), data) {
		t.Fatal("данные приехали не те")
	}
	for _, s := range dst.sizes {
		if s > bufSize {
			t.Fatalf("кусок %d больше буфера %d", s, bufSize)
		}
	}
	// 200 КБ буфером в 64 КБ — это единицы записей, а не десятки.
	if len(dst.sizes) > 8 {
		t.Fatalf("записей %d — буфер явно не тот, что дали", len(dst.sizes))
	}
}

// Ошибка чтения должна доезжать наверх вместе с уже переданными байтами:
// на ней держится закрытие второго направления.
func TestCopyBufReportsReadError(t *testing.T) {
	want := io.ErrUnexpectedEOF
	src := io.MultiReader(bytes.NewReader([]byte("привет")), errReader{want})
	dst := &sizeWriter{}

	n, err := copyBuf(dst, src, make([]byte, bufSize))
	if err != want {
		t.Fatalf("получили ошибку %v, ждали %v", err, want)
	}
	if n != int64(len("привет")) {
		t.Fatalf("до ошибки посчитано %d байт", n)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

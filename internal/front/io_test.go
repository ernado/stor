package front

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLimitReaderFrom(t *testing.T) {
	buf := new(bytes.Buffer)
	buf.WriteString("hello, world.")

	fileName := filepath.Join(t.TempDir(), "test.txt")
	require.NoError(t, os.WriteFile(fileName, buf.Bytes(), 0644))

	f, err := os.Open(fileName)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = f.Close()
	})

	lr := &LimitReaderFrom{
		R:      f,
		N:      5,
		Offset: 7,
	}
	out := new(bytes.Buffer)
	n, err := io.CopyBuffer(out, lr, make([]byte, 1024))
	require.NoError(t, err)
	require.Equal(t, int64(5), n)

	require.Equal(t, "world", out.String())
}

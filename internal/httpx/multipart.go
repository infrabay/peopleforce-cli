package httpx

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
)

// FieldValue is one multipart form field. If IsFile is true, Value is a
// filesystem path (already stripped of the CLI's @ prefix) and the field is
// written as a file part with a detected content type.
type FieldValue struct {
	Name   string
	Value  string
	IsFile bool
}

// EncodeMultipart builds a multipart/form-data body. Returns the body and
// the Content-Type header value (with boundary).
func EncodeMultipart(fields []FieldValue) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		if f.IsFile {
			if err := writeFilePart(w, f.Name, f.Value); err != nil {
				return nil, "", err
			}
			continue
		}
		if err := w.WriteField(f.Name, f.Value); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func writeFilePart(w *multipart.Writer, field, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	filename := filepath.Base(path)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	header.Set("Content-Type", detectContentType(path))
	part, err := w.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func detectContentType(path string) string {
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if ct == "" {
		return "application/octet-stream"
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return ct
}

// FileToDataURI reads a file and encodes it as the data-URI string the
// update_avatar endpoint expects inside its JSON body:
//
//	data:image/png;name=photo.png;base64,iVBOR...
func FileToDataURI(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// mime.TypeByExtension may append a charset; the API expects a bare type.
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return fmt.Sprintf("data:%s;name=%s;base64,%s",
		contentType, filepath.Base(path), base64.StdEncoding.EncodeToString(data)), nil
}

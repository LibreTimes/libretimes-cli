package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetProfileImageSendsMultipart(t *testing.T) {
	var path, auth, file, fileType, focus, crop string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.Method+" "+r.URL.Path, r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("not a multipart form: %v", err)
		}
		focus, crop = r.FormValue("focus"), r.FormValue("crop")
		f, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("no file part: %v", err)
		}
		data, _ := io.ReadAll(f)
		file, fileType = string(data), header.Header.Get("Content-Type")
		w.Write([]byte(`{"profile_image_url":"https://assets.example/p.webp","width":512,"height":512}`))
	}))
	defer server.Close()

	client := New(server.URL, "tok", "test")
	img, err := client.SetProfileImage(context.Background(), "me.jpg", "image/jpeg", []byte("JPEGBYTES"),
		ImageCrop{Focus: "0,0.4"})
	if err != nil {
		t.Fatalf("SetProfileImage: %v", err)
	}
	if path != "POST /account/profile-image" || auth != "Bearer tok" {
		t.Errorf("request = %q with %q", path, auth)
	}
	if file != "JPEGBYTES" || fileType != "image/jpeg" {
		t.Errorf("file part = %q (%s); the original bytes must go as they are", file, fileType)
	}
	if focus != "0,0.4" || crop != "" {
		t.Errorf("focus = %q, crop = %q; an empty field must not be sent", focus, crop)
	}
	if img.ProfileImageURL == nil || *img.ProfileImageURL != "https://assets.example/p.webp" || img.Width != 512 {
		t.Errorf("result = %+v", img)
	}
}

func TestSetProfileImageMapsRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"detail":{"detail":"The image is too small","error_code":"IMAGE_TOO_SMALL"}}`))
	}))
	defer server.Close()

	_, err := New(server.URL, "tok", "test").SetProfileImage(context.Background(), "x.png", "image/png",
		[]byte("x"), ImageCrop{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeValidation {
		t.Fatalf("err = %v; a 422 must map like any other request's", err)
	}
}

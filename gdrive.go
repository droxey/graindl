package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/md5"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── Drive REST API (stdlib-only, no SDK) ────────────────────────────────────
//
// Uses Google Drive REST API v3 directly via net/http to avoid pulling in
// the large google.golang.org/api dependency tree. Keeps the project at a
// single external dependency (go-rod/rod).

const (
	driveAPIBase    = "https://www.googleapis.com/drive/v3"
	driveUploadBase = "https://www.googleapis.com/upload/drive/v3"
	googleTokenURL  = "https://oauth2.googleapis.com/token"
)

// ── Sync State ──────────────────────────────────────────────────────────────

// DriveSyncState tracks which files have been uploaded to Google Drive.
// Persisted to .grain-session/gdrive-sync.json.
type DriveSyncState struct {
	Version  int                   `json:"version"`
	LastSync string                `json:"last_sync"`
	FolderID string                `json:"folder_id"`
	Files    map[string]*SyncEntry `json:"files"`
}

// SyncEntry records a single uploaded file's state.
type SyncEntry struct {
	DriveFileID  string `json:"drive_file_id"`
	MD5Checksum  string `json:"md5_checksum"`
	Size         int64  `json:"size"`
	LocalModTime string `json:"local_mod_time"`
	UploadedAt   string `json:"uploaded_at"`
}

// UploadStats summarizes the result of a batch upload operation.
type UploadStats struct {
	Created int
	Updated int
	Skipped int
}

// VerifyReport summarizes the result of a Drive-side verification.
type VerifyReport struct {
	InSync           int
	ReUploaded       int
	DeletedRemotely  int
	ModifiedRemotely int
	Untracked        int
}

// ── DriveUploader ───────────────────────────────────────────────────────────

// DriveUploader handles uploading files to Google Drive with incremental
// sync state tracking and conflict resolution.
type DriveUploader struct {
	client    *http.Client
	token     *oauthToken
	tokenMu   sync.Mutex
	folderID  string
	folderMap map[string]string // cache: relative dir path → Drive folder ID
	state     *DriveSyncState
	statePath string
	conflict  string // "local-wins", "skip", "newer-wins"
	mu        sync.Mutex

	// Fields for token refresh (user OAuth2 only).
	clientID     string
	clientSecret string
	refreshToken string
}

// oauthToken holds an access token and its expiry.
type oauthToken struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	Expiry       time.Time
}

// NewDriveUploader initializes a Google Drive uploader with authentication
// and loads any existing sync state.
func NewDriveUploader(ctx context.Context, cfg *Config) (*DriveUploader, error) {
	d := &DriveUploader{
		client:    &http.Client{Timeout: 5 * time.Minute},
		folderID:  cfg.GDriveFolderID,
		folderMap: map[string]string{".": cfg.GDriveFolderID},
		conflict:  cfg.GDriveConflict,
	}

	// Warn if credentials file has overly permissive permissions.
	if info, statErr := os.Stat(cfg.GDriveCredentials); statErr == nil {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			slog.Warn("Credentials file has wide permissions",
				"path", cfg.GDriveCredentials,
				"perms", fmt.Sprintf("%04o", perm))
		}
	}

	var err error
	if cfg.GDriveServiceAcct {
		err = d.authServiceAccount(ctx, cfg.GDriveCredentials)
	} else {
		err = d.authUserOAuth2(ctx, cfg.GDriveCredentials, cfg.GDriveTokenFile)
	}
	if err != nil {
		return nil, fmt.Errorf("drive auth: %w", err)
	}

	// Load sync state.
	statePath := filepath.Join(cfg.SessionDir, "gdrive-sync.json")
	state, err := loadDriveSyncState(statePath)
	if err != nil {
		return nil, fmt.Errorf("load sync state: %w", err)
	}

	// Detect folder ID change — reset state if user switched target folders.
	if state.FolderID != "" && state.FolderID != cfg.GDriveFolderID {
		slog.Warn("Drive folder ID changed, resetting sync state",
			"old", state.FolderID, "new", cfg.GDriveFolderID)
		state = &DriveSyncState{Version: 1, Files: make(map[string]*SyncEntry)}
	}
	state.FolderID = cfg.GDriveFolderID

	d.state = state
	d.statePath = statePath

	return d, nil
}

// ── Authentication ──────────────────────────────────────────────────────────

// serviceAccountKey is the JSON structure of a Google service account key file.
type serviceAccountKey struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

func (d *DriveUploader) authServiceAccount(ctx context.Context, credPath string) error {
	data, err := os.ReadFile(credPath)
	if err != nil {
		return fmt.Errorf("read credentials: %w", err)
	}

	var key serviceAccountKey
	if err := json.Unmarshal(data, &key); err != nil {
		return fmt.Errorf("parse service account key: %w", err)
	}
	if key.Type != "service_account" {
		return fmt.Errorf("expected service_account type, got %q", key.Type)
	}

	tokenURI := key.TokenURI
	if tokenURI == "" {
		tokenURI = googleTokenURL
	}

	// Parse RSA private key.
	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		return fmt.Errorf("failed to decode PEM block from private key")
	}
	privKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	rsaKey, ok := privKey.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("private key is not RSA")
	}

	// Create signed JWT.
	tok, err := exchangeJWT(ctx, d.client, rsaKey, key.ClientEmail, tokenURI)
	if err != nil {
		return err
	}
	d.token = tok
	return nil
}

// exchangeJWT creates a JWT assertion and exchanges it for an access token.
func exchangeJWT(ctx context.Context, client *http.Client, key *rsa.PrivateKey, email, tokenURI string) (*oauthToken, error) {
	now := time.Now()
	header := base64URLEncode([]byte(`{"alg":"RS256","typ":"JWT"}`))

	claims, _ := json.Marshal(map[string]any{
		"iss":   email,
		"scope": "https://www.googleapis.com/auth/drive.file",
		"aud":   tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	payload := header + "." + base64URLEncode(claims)

	// Sign with RSA-SHA256.
	h := sha256.Sum256([]byte(payload))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, h[:])
	if err != nil {
		return nil, fmt.Errorf("sign JWT: %w", err)
	}
	jwt := payload + "." + base64URLEncode(sig)

	// Exchange JWT for access token.
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {jwt},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := readErrorBody(resp.Body)
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, body)
	}

	var tok oauthToken
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	tok.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return &tok, nil
}

// oauthClientCreds is the JSON structure of a Google OAuth2 client credentials file.
type oauthClientCreds struct {
	Installed *oauthClientConfig `json:"installed"`
	Web       *oauthClientConfig `json:"web"`
}

type oauthClientConfig struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	AuthURI      string   `json:"auth_uri"`
	TokenURI     string   `json:"token_uri"`
	RedirectURIs []string `json:"redirect_uris"`
}

func (d *DriveUploader) authUserOAuth2(ctx context.Context, credPath, tokenPath string) error {
	data, err := os.ReadFile(credPath)
	if err != nil {
		return fmt.Errorf("read credentials: %w", err)
	}

	var creds oauthClientCreds
	if err := json.Unmarshal(data, &creds); err != nil {
		return fmt.Errorf("parse oauth config: %w", err)
	}

	cfg := creds.Installed
	if cfg == nil {
		cfg = creds.Web
	}
	if cfg == nil {
		return fmt.Errorf("credentials file must contain 'installed' or 'web' config")
	}

	d.clientID = cfg.ClientID
	d.clientSecret = cfg.ClientSecret

	tokenURI := cfg.TokenURI
	if tokenURI == "" {
		tokenURI = googleTokenURL
	}

	// Try to load cached token.
	tok, err := loadCachedToken(tokenPath)
	if err == nil && tok.AccessToken != "" {
		d.token = tok
		d.refreshToken = tok.RefreshToken
		return nil
	}

	// Interactive OAuth2 flow.
	authURI := cfg.AuthURI
	if authURI == "" {
		authURI = "https://accounts.google.com/o/oauth2/auth"
	}

	redirectURI := "urn:ietf:wg:oauth:2.0:oob"
	authURL := fmt.Sprintf("%s?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&access_type=offline",
		authURI,
		url.QueryEscape(cfg.ClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape("https://www.googleapis.com/auth/drive.file"),
	)

	fmt.Printf("Open this URL in your browser and enter the authorization code:\n%s\n\nCode: ", authURL)
	var code string
	if _, err := fmt.Scan(&code); err != nil {
		return fmt.Errorf("read auth code: %w", err)
	}

	// Exchange code for token.
	form := url.Values{
		"code":          {code},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := readErrorBody(resp.Body)
		return fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, body)
	}

	tok = &oauthToken{}
	if err := json.NewDecoder(resp.Body).Decode(tok); err != nil {
		return fmt.Errorf("decode token: %w", err)
	}
	tok.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	d.token = tok
	d.refreshToken = tok.RefreshToken

	// Cache token.
	if err := saveCachedToken(tokenPath, tok); err != nil {
		slog.Warn("Failed to cache OAuth2 token", "error", err)
	}

	return nil
}

func loadCachedToken(path string) (*oauthToken, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok oauthToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func saveCachedToken(path string, tok *oauthToken) error {
	if err := ensureDirPrivate(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// accessToken returns a valid access token, refreshing if expired.
func (d *DriveUploader) accessToken(ctx context.Context) (string, error) {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()

	if d.token != nil && time.Now().Before(d.token.Expiry.Add(-1*time.Minute)) {
		return d.token.AccessToken, nil
	}

	// Token expired — refresh.
	if d.refreshToken != "" {
		tok, err := d.refreshAccessToken(ctx)
		if err != nil {
			return "", fmt.Errorf("refresh token: %w", err)
		}
		d.token = tok
		return tok.AccessToken, nil
	}

	if d.token != nil {
		return d.token.AccessToken, nil
	}

	return "", fmt.Errorf("no valid access token")
}

func (d *DriveUploader) refreshAccessToken(ctx context.Context) (*oauthToken, error) {
	form := url.Values{
		"client_id":     {d.clientID},
		"client_secret": {d.clientSecret},
		"refresh_token": {d.refreshToken},
		"grant_type":    {"refresh_token"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := readErrorBody(resp.Body)
		return nil, fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, body)
	}

	var tok oauthToken
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, err
	}
	tok.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if tok.RefreshToken == "" {
		tok.RefreshToken = d.refreshToken
	}
	return &tok, nil
}

// ── Drive API Calls ─────────────────────────────────────────────────────────

// driveFile represents a Google Drive file in API responses.
type driveFile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MIMEType    string `json:"mimeType"`
	MD5Checksum string `json:"md5Checksum"`
}

// driveFileList represents a Google Drive file list response.
type driveFileList struct {
	Files         []driveFile `json:"files"`
	NextPageToken string      `json:"nextPageToken"`
}

func (d *DriveUploader) driveRequest(ctx context.Context, method, url string, body io.Reader, contentType string) (*http.Response, error) {
	token, err := d.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	return d.client.Do(req)
}

func (d *DriveUploader) listFiles(ctx context.Context, parentID, pageToken string) (*driveFileList, error) {
	q := url.QueryEscape(fmt.Sprintf("'%s' in parents and trashed = false", parentID))
	fields := url.QueryEscape("nextPageToken, files(id, name, md5Checksum, mimeType)")
	apiURL := fmt.Sprintf("%s/files?q=%s&fields=%s&pageSize=100", driveAPIBase, q, fields)
	if pageToken != "" {
		apiURL += "&pageToken=" + url.QueryEscape(pageToken)
	}

	resp, err := d.driveRequest(ctx, "GET", apiURL, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := readErrorBody(resp.Body)
		return nil, fmt.Errorf("list files failed (%d): %s", resp.StatusCode, body)
	}

	var list driveFileList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return &list, nil
}

func (d *DriveUploader) createFolder(ctx context.Context, name, parentID string) (string, error) {
	meta := map[string]any{
		"name":     name,
		"mimeType": "application/vnd.google-apps.folder",
		"parents":  []string{parentID},
	}
	body, _ := json.Marshal(meta)

	resp, err := d.driveRequest(ctx, "POST", driveAPIBase+"/files?fields=id", bytes.NewReader(body), "application/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody := readErrorBody(resp.Body)
		return "", fmt.Errorf("create folder failed (%d): %s", resp.StatusCode, respBody)
	}

	var result driveFile
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.ID, nil
}

// uploadFile creates or updates a file on Drive using multipart upload.
func (d *DriveUploader) uploadFile(ctx context.Context, localPath, fileName, mimeType, parentID, existingID string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Part 1: JSON metadata.
	metaHeader := make(textproto.MIMEHeader)
	metaHeader.Set("Content-Type", "application/json; charset=UTF-8")
	metaPart, err := w.CreatePart(metaHeader)
	if err != nil {
		return "", err
	}
	meta := map[string]any{"name": fileName}
	if existingID == "" {
		meta["parents"] = []string{parentID}
	}
	json.NewEncoder(metaPart).Encode(meta)

	// Part 2: file content.
	fileHeader := make(textproto.MIMEHeader)
	fileHeader.Set("Content-Type", mimeType)
	filePart, err := w.CreatePart(fileHeader)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(filePart, f); err != nil {
		return "", err
	}
	w.Close()

	var apiURL string
	var method string
	if existingID != "" {
		apiURL = fmt.Sprintf("%s/files/%s?uploadType=multipart&fields=id,md5Checksum", driveUploadBase, existingID)
		method = "PATCH"
	} else {
		apiURL = fmt.Sprintf("%s/files?uploadType=multipart&fields=id,md5Checksum", driveUploadBase)
		method = "POST"
	}

	contentType := "multipart/related; boundary=" + w.Boundary()
	resp, err := d.driveRequest(ctx, method, apiURL, &buf, contentType)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := readErrorBody(resp.Body)
		return "", &driveAPIError{Code: resp.StatusCode, Body: string(body)}
	}

	var result driveFile
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.ID, nil
}

// driveAPIError represents an HTTP error from the Drive API.
type driveAPIError struct {
	Code int
	Body string
}

func (e *driveAPIError) Error() string {
	return fmt.Sprintf("drive API error (%d): %s", e.Code, e.Body)
}

// ── Sync State Persistence ──────────────────────────────────────────────────

func loadDriveSyncState(path string) (*DriveSyncState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &DriveSyncState{Version: 1, Files: make(map[string]*SyncEntry)}, nil
	}
	if err != nil {
		return nil, err
	}
	var state DriveSyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal sync state: %w", err)
	}
	if state.Files == nil {
		state.Files = make(map[string]*SyncEntry)
	}
	return &state, nil
}

func (d *DriveUploader) saveSyncState() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.state.LastSync = time.Now().UTC().Format(time.RFC3339)

	data, err := json.MarshalIndent(d.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sync state: %w", err)
	}

	// Atomic write: temp file + rename.
	tmp := d.statePath + ".tmp"
	if err := writeFile(tmp, data); err != nil {
		return fmt.Errorf("write temp sync state: %w", err)
	}
	if err := os.Rename(tmp, d.statePath); err != nil {
		return fmt.Errorf("rename sync state: %w", err)
	}
	return nil
}

// ── Upload Decision ─────────────────────────────────────────────────────────

// shouldUpload decides whether a local file needs uploading.
// Returns action ("create", "update", or "skip") and the existing SyncEntry (if any).
func (d *DriveUploader) shouldUpload(localPath, relPath string) (string, *SyncEntry) {
	checksum, err := md5File(localPath)
	if err != nil {
		slog.Warn("MD5 computation failed, will create", "path", localPath, "error", err)
		return "create", nil
	}

	d.mu.Lock()
	entry, exists := d.state.Files[relPath]
	d.mu.Unlock()

	if !exists {
		return "create", nil
	}

	if entry.MD5Checksum == checksum {
		return "skip", entry
	}

	// File changed — apply conflict strategy.
	switch d.conflict {
	case "skip":
		return "skip", entry
	case "newer-wins":
		info, err := os.Stat(localPath)
		if err != nil {
			return "update", entry
		}
		uploadedAt, err := time.Parse(time.RFC3339, entry.UploadedAt)
		if err != nil {
			return "update", entry
		}
		if info.ModTime().After(uploadedAt) {
			return "update", entry
		}
		return "skip", entry
	default: // "local-wins"
		return "update", entry
	}
}

// ── Folder Management ───────────────────────────────────────────────────────

// EnsureFolder creates the folder hierarchy on Drive and returns the leaf
// folder ID. Results are cached to avoid redundant API calls.
func (d *DriveUploader) EnsureFolder(ctx context.Context, relDir string) (string, error) {
	if relDir == "" || relDir == "." {
		return d.folderID, nil
	}

	d.mu.Lock()
	if id, ok := d.folderMap[relDir]; ok {
		d.mu.Unlock()
		return id, nil
	}
	d.mu.Unlock()

	// Walk path components to create nested folders.
	parts := strings.Split(filepath.ToSlash(relDir), "/")
	parentID := d.folderID
	accumulated := ""

	for _, part := range parts {
		if accumulated == "" {
			accumulated = part
		} else {
			accumulated = accumulated + "/" + part
		}

		d.mu.Lock()
		if id, ok := d.folderMap[accumulated]; ok {
			parentID = id
			d.mu.Unlock()
			continue
		}
		d.mu.Unlock()

		// Check if folder already exists on Drive.
		list, err := d.listFiles(ctx, parentID, "")
		if err != nil {
			return "", fmt.Errorf("list folders: %w", err)
		}

		var folderID string
		for _, f := range list.Files {
			if f.Name == part && f.MIMEType == "application/vnd.google-apps.folder" {
				folderID = f.ID
				break
			}
		}

		if folderID == "" {
			// Create folder.
			folderID, err = d.createFolder(ctx, part, parentID)
			if err != nil {
				return "", fmt.Errorf("create folder %q: %w", part, err)
			}
			slog.Debug("Created Drive folder", "name", part, "id", folderID)
		}

		d.mu.Lock()
		d.folderMap[accumulated] = folderID
		d.mu.Unlock()
		parentID = folderID
	}

	return parentID, nil
}

// ── Core Upload ─────────────────────────────────────────────────────────────

// Upload uploads a single file to Google Drive with sync-aware logic.
// Returns the Drive file ID.
func (d *DriveUploader) Upload(ctx context.Context, localPath, relPath string) (string, error) {
	action, entry := d.shouldUpload(localPath, relPath)
	if action == "skip" {
		slog.Debug("Drive upload skipped (in sync)", "path", relPath)
		return "", nil
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", localPath, err)
	}

	relDir := filepath.Dir(relPath)
	parentID, err := d.EnsureFolder(ctx, relDir)
	if err != nil {
		return "", fmt.Errorf("ensure folder %s: %w", relDir, err)
	}

	mimeType := detectMIME(localPath)
	fileName := filepath.Base(localPath)

	var existingID string
	if action == "update" && entry != nil {
		existingID = entry.DriveFileID
	}

	driveFileID, err := d.retryUpload(ctx, localPath, fileName, mimeType, parentID, existingID)
	if err != nil {
		return "", err
	}

	if action == "update" {
		slog.Debug("Drive file updated", "path", relPath, "id", driveFileID)
	} else {
		slog.Debug("Drive file created", "path", relPath, "id", driveFileID)
	}

	// Update sync state in memory.
	checksum, _ := md5File(localPath)
	d.mu.Lock()
	d.state.Files[relPath] = &SyncEntry{
		DriveFileID:  driveFileID,
		MD5Checksum:  checksum,
		Size:         info.Size(),
		LocalModTime: info.ModTime().UTC().Format(time.RFC3339),
		UploadedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	d.mu.Unlock()

	return driveFileID, nil
}

// retryUpload wraps a Drive upload with exponential backoff for transient errors.
func (d *DriveUploader) retryUpload(ctx context.Context, localPath, fileName, mimeType, parentID, existingID string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt)) * time.Second
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}

		id, err := d.uploadFile(ctx, localPath, fileName, mimeType, parentID, existingID)
		if err == nil {
			return id, nil
		}
		lastErr = err

		if apiErr, ok := err.(*driveAPIError); ok && isTransientCode(apiErr.Code) {
			slog.Debug("Retrying Drive upload", "attempt", attempt+1, "error", err)
			continue
		}
		return "", err
	}
	return "", lastErr
}

func isTransientCode(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusInternalServerError ||
		code == http.StatusServiceUnavailable
}

// ── Batch Operations ────────────────────────────────────────────────────────

// UploadExportResult uploads all files referenced by an ExportResult.
func (d *DriveUploader) UploadExportResult(ctx context.Context, outputDir string, r *ExportResult) (*UploadStats, error) {
	stats := &UploadStats{}

	paths := collectResultPaths(r)

	for _, relPath := range paths {
		if relPath == "" {
			continue
		}
		localPath := filepath.Join(outputDir, relPath)
		if !fileExists(localPath) {
			continue
		}

		action, _ := d.shouldUpload(localPath, relPath)
		switch action {
		case "skip":
			stats.Skipped++
			continue
		case "update":
			stats.Updated++
		case "create":
			stats.Created++
		}

		if _, err := d.Upload(ctx, localPath, relPath); err != nil {
			return stats, fmt.Errorf("upload %s: %w", relPath, err)
		}
	}
	return stats, nil
}

// collectResultPaths gathers all file paths from an ExportResult.
func collectResultPaths(r *ExportResult) []string {
	var paths []string
	paths = append(paths, r.MetadataPath)
	for _, p := range r.TranscriptPaths {
		paths = append(paths, p)
	}
	paths = append(paths, r.HighlightsPath)
	paths = append(paths, r.MarkdownPath)
	paths = append(paths, r.VideoPath)
	paths = append(paths, r.AudioPath)
	return paths
}

// UploadManifest uploads the export manifest file.
func (d *DriveUploader) UploadManifest(ctx context.Context, outputDir, manifestPath string) error {
	relPath, err := filepath.Rel(outputDir, manifestPath)
	if err != nil {
		relPath = filepath.Base(manifestPath)
	}
	_, err = d.Upload(ctx, manifestPath, relPath)
	return err
}

// ── Verification ────────────────────────────────────────────────────────────

// Verify reconciles local sync state against actual files on Drive.
func (d *DriveUploader) Verify(ctx context.Context, outputDir string) (*VerifyReport, error) {
	report := &VerifyReport{}

	driveFiles, err := d.listAllFiles(ctx, d.folderID)
	if err != nil {
		return nil, fmt.Errorf("list drive files: %w", err)
	}

	// Build lookup: Drive file ID → driveFile.
	driveByID := make(map[string]driveFile, len(driveFiles))
	for _, f := range driveFiles {
		driveByID[f.ID] = f
	}

	d.mu.Lock()
	stateFiles := make(map[string]*SyncEntry, len(d.state.Files))
	for k, v := range d.state.Files {
		stateFiles[k] = v
	}
	d.mu.Unlock()

	for relPath, entry := range stateFiles {
		df, exists := driveByID[entry.DriveFileID]
		if !exists {
			report.DeletedRemotely++
			localPath := filepath.Join(outputDir, relPath)
			if !fileExists(localPath) {
				continue
			}
			slog.Info("Re-uploading file deleted from Drive", "path", relPath)
			d.mu.Lock()
			delete(d.state.Files, relPath)
			d.mu.Unlock()
			if _, err := d.Upload(ctx, localPath, relPath); err != nil {
				slog.Warn("Re-upload failed", "path", relPath, "error", err)
			} else {
				report.ReUploaded++
			}
			continue
		}

		if df.MD5Checksum == entry.MD5Checksum {
			report.InSync++
			delete(driveByID, entry.DriveFileID)
			continue
		}

		report.ModifiedRemotely++
		delete(driveByID, entry.DriveFileID)

		if d.conflict == "skip" {
			slog.Debug("Skipping Drive-modified file", "path", relPath)
			continue
		}

		localPath := filepath.Join(outputDir, relPath)
		if !fileExists(localPath) {
			continue
		}
		if _, err := d.Upload(ctx, localPath, relPath); err != nil {
			slog.Warn("Re-upload of modified file failed", "path", relPath, "error", err)
		} else {
			report.ReUploaded++
		}
	}

	report.Untracked = len(driveByID)
	if report.Untracked > 0 {
		slog.Debug("Untracked files on Drive", "count", report.Untracked)
	}

	return report, nil
}

func (d *DriveUploader) listAllFiles(ctx context.Context, folderID string) ([]driveFile, error) {
	var allFiles []driveFile
	pageToken := ""

	for {
		list, err := d.listFiles(ctx, folderID, pageToken)
		if err != nil {
			return nil, err
		}

		for _, f := range list.Files {
			if f.MIMEType == "application/vnd.google-apps.folder" {
				subFiles, err := d.listAllFiles(ctx, f.ID)
				if err != nil {
					return nil, err
				}
				allFiles = append(allFiles, subFiles...)
			} else {
				allFiles = append(allFiles, f)
			}
		}

		pageToken = list.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return allFiles, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// readErrorBody reads up to 64KB of a response body for error messages,
// preventing unbounded reads from consuming memory.
func readErrorBody(r io.Reader) []byte {
	body, _ := io.ReadAll(io.LimitReader(r, 64*1024))
	return body
}

func md5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func detectMIME(path string) string {
	ext := filepath.Ext(path)
	switch ext {
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".mp4":
		return "video/mp4"
	case ".m4a":
		return "audio/mp4"
	case ".webm":
		return "video/webm"
	case ".url":
		return "text/plain"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

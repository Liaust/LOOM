package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
)

const (
	defaultDirName    = "loom-node-agent"
	defaultConfigFile = "config.json"
	defaultStateFile  = "state.json"
)

type Store struct {
	ConfigPath string
	StatePath  string
	DataDir    string

	// A value-scoped token is passed only through one held sync transaction.
	syncLocked bool
}

type LocalQueueCounts struct {
	Inbox  int `json:"inbox"`
	Outbox int `json:"outbox"`
}

func ResolveStore(configPath, statePath, dataDir string) (Store, error) {
	var err error
	if configPath == "" {
		configPath, err = DefaultConfigPath()
		if err != nil {
			return Store{}, err
		}
	}
	if statePath == "" {
		statePath, err = DefaultStatePath()
		if err != nil {
			return Store{}, err
		}
	}
	if dataDir == "" {
		dataDir, err = DefaultDataDir()
		if err != nil {
			return Store{}, err
		}
	}
	return Store{
		ConfigPath: configPath,
		StatePath:  statePath,
		DataDir:    dataDir,
	}, nil
}

func DefaultConfigPath() (string, error) {
	dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if dir == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("resolve user config directory: %w", homeErr)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, defaultDirName, defaultConfigFile), nil
}

func DefaultStatePath() (string, error) {
	dir, err := defaultStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, defaultDirName, defaultStateFile), nil
}

func DefaultDataDir() (string, error) {
	dir, err := defaultStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, defaultDirName), nil
}

func defaultStateDir() (string, error) {
	dir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if dir == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("resolve user state directory: %w", homeErr)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return dir, nil
}

func (s Store) LoadConfig() (Config, error) {
	var config Config
	if err := readJSONFile(s.ConfigPath, &config); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, fmt.Errorf("node-agent config is not initialized at %s", s.ConfigPath)
		}
		return Config{}, err
	}
	config = normalizeConfig(config)
	if err := validateOptionalAbsolutePath("box_root_path", config.BoxRootPath); err != nil {
		return Config{}, err
	}
	if err := validateOptionalAbsolutePath("box_state_root", config.BoxStateRoot); err != nil {
		return Config{}, err
	}
	if err := filesystemconnector.ValidateConfig(config.Filesystem); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (s Store) SaveConfig(config Config) error {
	config = normalizeConfig(config)
	if err := validateOptionalAbsolutePath("box_root_path", config.BoxRootPath); err != nil {
		return err
	}
	if err := validateOptionalAbsolutePath("box_state_root", config.BoxStateRoot); err != nil {
		return err
	}
	if err := filesystemconnector.ValidateConfig(config.Filesystem); err != nil {
		return err
	}
	if config.MainURL == "" {
		return errors.New("main_url is required")
	}
	if config.NodeKey == "" {
		return errors.New("node_key is required")
	}
	if config.DisplayName == "" {
		return errors.New("display_name is required")
	}
	if _, err := normalizeMainURL(config.MainURL); err != nil {
		return err
	}
	now := time.Now().UTC()
	if config.CreatedAt.IsZero() {
		config.CreatedAt = now
	}
	config.UpdatedAt = now
	return writeJSONFile(s.ConfigPath, config, 0o600)
}

func (s Store) LoadState() (State, error) {
	var state State
	if err := readJSONFile(s.StatePath, &state); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return State{}, nil
		}
		return State{}, err
	}
	return state, nil
}

func (s Store) SaveState(state State) error {
	state.UpdatedAt = time.Now().UTC()
	return writeJSONFile(s.StatePath, state, 0o600)
}

func (s Store) EnsureDataDirs() error {
	for _, dir := range []string{
		s.DataDir,
		filepath.Join(s.DataDir, "inbox"),
		filepath.Join(s.DataDir, "outbox"),
		filepath.Join(s.DataDir, "logs"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) SaveInboxMessage(message communication.Message) (string, error) {
	if err := s.EnsureDataDirs(); err != nil {
		return "", err
	}
	path := filepath.Join(s.DataDir, "inbox", message.CommunicationMessageID+".json")
	return path, writeJSONFile(path, message, 0o600)
}

func (s Store) SaveOutboxAck(result communication.AckResult) (string, error) {
	if err := s.EnsureDataDirs(); err != nil {
		return "", err
	}
	name := result.Ack.CommunicationAckID
	if strings.TrimSpace(name) == "" {
		name = result.Message.CommunicationMessageID
	}
	path := filepath.Join(s.DataDir, "outbox", name+".json")
	return path, writeJSONFile(path, result, 0o600)
}

func (s Store) SaveOutboxCapabilityResult(result response.Envelope[routing.RemoteResultOutcome]) (string, error) {
	if err := s.EnsureDataDirs(); err != nil {
		return "", err
	}
	name := result.Data.MessageID
	if strings.TrimSpace(name) == "" {
		name = result.Data.CapabilityCall.CapabilityCallID
	}
	if strings.TrimSpace(name) == "" {
		name = "capability-result-" + time.Now().UTC().Format("20060102150405")
	}
	path := filepath.Join(s.DataDir, "outbox", name+".json")
	return path, writeJSONFile(path, result, 0o600)
}

func (s Store) QueueCounts() (LocalQueueCounts, error) {
	if err := s.EnsureDataDirs(); err != nil {
		return LocalQueueCounts{}, err
	}
	inbox, err := countJSONFiles(filepath.Join(s.DataDir, "inbox"))
	if err != nil {
		return LocalQueueCounts{}, err
	}
	outbox, err := countJSONFiles(filepath.Join(s.DataDir, "outbox"))
	if err != nil {
		return LocalQueueCounts{}, err
	}
	return LocalQueueCounts{Inbox: inbox, Outbox: outbox}, nil
}

func countJSONFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

func normalizeConfig(config Config) Config {
	config.MainURL = strings.TrimRight(strings.TrimSpace(config.MainURL), "/")
	config.NodeKey = strings.TrimSpace(config.NodeKey)
	config.DisplayName = strings.TrimSpace(config.DisplayName)
	config.NodeKind = strings.TrimSpace(config.NodeKind)
	config.NodeRole = strings.TrimSpace(config.NodeRole)
	config.RuntimeClass = strings.TrimSpace(config.RuntimeClass)
	config.BoxRootPath = cleanOptionalPath(config.BoxRootPath)
	config.BoxStateRoot = cleanOptionalPath(config.BoxStateRoot)
	if config.NodeKind == "" {
		config.NodeKind = defaultNodeKind
	}
	if config.NodeRole == "" {
		config.NodeRole = defaultNodeRole
	}
	if config.RuntimeClass == "" {
		config.RuntimeClass = defaultRuntimeClass
	}
	if config.HeartbeatIntervalSeconds <= 0 {
		config.HeartbeatIntervalSeconds = defaultHeartbeatIntervalSeconds
	}
	if config.PollIntervalSeconds <= 0 {
		config.PollIntervalSeconds = defaultPollIntervalSeconds
	}
	config.BackupTransport = normalizeBackupTransportConfig(config.BackupTransport)
	config.Filesystem = filesystemconnector.NormalizeConfig(config.Filesystem)
	return config
}

func cleanOptionalPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func validateOptionalAbsolutePath(name, value string) error {
	if value == "" {
		return nil
	}
	if !filepath.IsAbs(value) || filepath.Clean(value) == string(filepath.Separator) {
		return fmt.Errorf("%s must be an absolute path below the filesystem root", name)
	}
	return nil
}

func normalizeMainURL(rawURL string) (string, error) {
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if rawURL == "" {
		return "", errors.New("main_url is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse main_url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("main_url must use http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("main_url must include a host")
	}
	return rawURL, nil
}

func readJSONFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSONFile(path string, value any, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, perm); err != nil {
		return err
	}
	cleanup = false
	return nil
}

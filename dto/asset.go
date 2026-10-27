package dto

// AssetActor contains authenticated identity and the channels currently allowed
// by the host's token, model and group policies. It never contains credentials.
type AssetActor struct {
	UserID     int
	TokenID    int
	ChannelIDs []int
	AllUsers   bool
}

type AssetStorageFields struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider,omitempty"`
	Endpoint       string `json:"endpoint"`
	Region         string `json:"region"`
	Bucket         string `json:"bucket"`
	AccessKey      string `json:"access_key"`
	ForcePathStyle bool   `json:"force_path_style"`
	Revision       int64  `json:"revision"`
}

type AssetStorageUpdate struct {
	AssetStorageFields
	SecretKey *string `json:"secret_key,omitempty"`
}

type AssetStorageView struct {
	AssetStorageFields
	HasSecret bool `json:"has_secret"`
}

type AssetChannelFields struct {
	Enabled      bool            `json:"enabled"`
	Endpoint     string          `json:"endpoint"`
	ProjectName  string          `json:"project_name"`
	AuthType     string          `json:"auth_type"`
	AccessKey    string          `json:"access_key"`
	Revision     int64           `json:"revision"`
	SizeLimitsMB AssetSizeLimits `json:"size_limits_mb"`
}

// Zero limits inherit the Ark defaults. MB is decimal; images use an exclusive
// upper bound, while video and audio include the configured boundary.
type AssetSizeLimits struct {
	Image int64 `json:"image"`
	Video int64 `json:"video"`
	Audio int64 `json:"audio"`
}

func (limits AssetSizeLimits) WithDefaults() AssetSizeLimits {
	if limits.Image == 0 {
		limits.Image = 30
	}
	if limits.Video == 0 {
		limits.Video = 200
	}
	if limits.Audio == 0 {
		limits.Audio = 15
	}
	return limits
}

func (limits AssetSizeLimits) ByteLimit(assetType string) int64 {
	limits = limits.WithDefaults()
	switch assetType {
	case "Image":
		return limits.Image*1_000_000 - 1
	case "Video":
		return limits.Video * 1_000_000
	case "Audio":
		return limits.Audio * 1_000_000
	default:
		return 0
	}
}

type AssetChannelUpdate struct {
	AssetChannelFields
	SecretKey *string `json:"secret_key,omitempty"`
}

type AssetChannelView struct {
	AssetChannelFields
	ChannelID int  `json:"channel_id"`
	HasSecret bool `json:"has_secret"`
}

type AssetUpload struct {
	GroupID     string
	ProjectName string
	Name        string
	AssetType   string
	Size        int64
	ContentType string
}

type AssetFailure struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

type AssetGroupView struct {
	UserID      int    `json:"UserId,omitempty"`
	ID          string `json:"Id"`
	Name        string `json:"Name"`
	Description string `json:"Description"`
	GroupType   string `json:"GroupType"`
	ProjectName string `json:"ProjectName"`
	CreateTime  string `json:"CreateTime"`
	UpdateTime  string `json:"UpdateTime"`
}

type AssetView struct {
	UserID            int               `json:"UserId,omitempty"`
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	GroupID           string            `json:"GroupId"`
	AssetType         string            `json:"AssetType"`
	ProjectName       string            `json:"ProjectName"`
	Status            string            `json:"Status"`
	URL               string            `json:"URL"`
	Moderation        map[string]string `json:"Moderation,omitempty"`
	Error             *AssetFailure     `json:"Error,omitempty"`
	CreateTime        string            `json:"CreateTime"`
	UpdateTime        string            `json:"UpdateTime"`
	LastInferenceTime string            `json:"LastInferenceTime,omitempty"`
}

type AssetReplicaView struct {
	ChannelID         int    `json:"channel_id"`
	ChannelName       string `json:"channel_name"`
	Status            string `json:"status"`
	ErrorCode         string `json:"error_code"`
	ErrorMessage      string `json:"error_message"`
	LastAttemptAt     int64  `json:"last_attempt_at"`
	CanRetry          bool   `json:"can_retry"`
	FileSizeBytes     *int64 `json:"file_size_bytes,omitempty"`
	CurrentLimitBytes *int64 `json:"current_limit_bytes,omitempty"`
}

type ResolvedAssetReference struct {
	ID         string `json:"id"`
	UpstreamID string `json:"upstreamId"`
	AssetType  string `json:"type"`
}

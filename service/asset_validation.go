package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
)

const assetValidationCallbackPath = "/api/assets/validation/callback/"

type assetValidationSecrets struct {
	BytedToken  string
	H5Link      string
	CallbackURL string
}

func assetValidationCipher() (cipher.AEAD, error) {
	key := sha256.Sum256([]byte("new-api:asset-validation:v1\x00" + common.CryptoSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealAssetValidationSecrets(sessionID string, secrets assetValidationSecrets) (string, error) {
	aead, err := assetValidationCipher()
	if err != nil {
		return "", err
	}
	data, err := common.Marshal(secrets)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, data, []byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func openAssetValidationSecrets(session *model.AssetValidationSession) (assetValidationSecrets, error) {
	var secrets assetValidationSecrets
	aead, err := assetValidationCipher()
	if err != nil {
		return secrets, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(session.SecretCipher)
	if err != nil || len(sealed) < aead.NonceSize()+aead.Overhead() {
		return secrets, assetError(http.StatusServiceUnavailable, "ValidationSecretUnavailable", "The verification session cannot be restored with the current secret")
	}
	data, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(session.PublicID))
	if err != nil || common.Unmarshal(data, &secrets) != nil {
		return secrets, assetError(http.StatusServiceUnavailable, "ValidationSecretUnavailable", "The verification session cannot be restored with the current secret")
	}
	return secrets, nil
}

func assetValidationHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func assetValidationError(err error) error {
	switch {
	case errors.Is(err, model.ErrAssetValidationThrottled):
		return assetError(http.StatusTooManyRequests, "Throttling", "Human verification is busy; retry shortly")
	case errors.Is(err, model.ErrUserSessionInactive):
		return assetError(http.StatusForbidden, "AccessDenied", "The original verification authorization is no longer valid")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return assetError(http.StatusNotFound, "ResourceNotFound", "The verification session was not found")
	default:
		return err
	}
}

func authorizeAssetValidation(ctx context.Context, actor dto.AssetActor, session *model.AssetValidationSession) error {
	if actor.AllUsers || actor.UserID != session.UserID || actor.InitiatorID != session.InitiatorID || actor.TokenID != session.TokenID || actor.AccessTokenID != session.AccessTokenID ||
		actor.AuthSessionID != session.AuthSessionID || actor.UserAuthVersion != session.UserAuthVersion || actor.SessionVersion != session.SessionVersion {
		return assetError(http.StatusForbidden, "AccessDenied", "The original verification authorization is no longer valid")
	}
	if actor.TokenID > 0 && !slices.Contains(actor.ChannelIDs, session.ChannelID) {
		return assetError(http.StatusForbidden, "AccessDenied", "The verification channel is unavailable to this caller")
	}
	allowed, err := AssetChannelAuthorized(ctx, session.UserID, session.TokenID, session.ChannelID)
	if err != nil {
		return err
	}
	if !allowed {
		return assetError(http.StatusForbidden, "AccessDenied", "The verification channel is unavailable to this caller")
	}
	return assetValidationError(model.ValidateAssetValidationSession(ctx, session))
}

func authorizeAssetValidationChannelWithDB(ctx context.Context, tx *gorm.DB, session *model.AssetValidationSession) error {
	ids, err := authorizedAssetChannelsWithDB(ctx, tx, session.UserID, session.TokenID, session.ChannelID)
	if err != nil {
		return err
	}
	if !slices.Contains(ids, session.ChannelID) {
		return assetError(http.StatusForbidden, "AccessDenied", "The verification channel is unavailable to this caller")
	}
	return nil
}

func assetValidationView(session *model.AssetValidationSession) (dto.AssetValidationView, error) {
	view := dto.AssetValidationView{BytedToken: session.PublicID, Status: session.Status, GroupID: session.GroupID, ExpiresAt: session.ExpiresAt, Name: session.Name, ChannelID: session.ChannelID}
	if session.ErrorCode != "" {
		view.Error = &dto.AssetFailure{Code: session.ErrorCode, Message: session.ErrorMessage}
	}
	if session.Status == model.AssetValidationCreated {
		return view, nil
	}
	if session.ExpiresAt <= time.Now().Unix() && session.UpstreamGroupID == "" {
		view.Status = model.AssetValidationExpired
		return view, nil
	}
	if session.Status == model.AssetValidationCreating && session.LeaseExpiresAt <= time.Now().Unix() {
		view.Status = model.AssetValidationUnknown
	}
	if session.SecretCipher != "" && session.ExpiresAt > time.Now().Unix() {
		secrets, err := openAssetValidationSecrets(session)
		if err != nil {
			return view, err
		}
		view.H5Link, view.CallbackURL = secrets.H5Link, secrets.CallbackURL
	}
	return view, nil
}

func CreateAssetValidationSession(ctx context.Context, actor dto.AssetActor, input dto.AssetValidationCreate) (dto.AssetValidationView, error) {
	var empty dto.AssetValidationView
	if actor.UserID <= 0 || actor.InitiatorID <= 0 || actor.AllUsers || (actor.TokenID == 0 && actor.AuthSessionID == "") {
		return empty, assetError(http.StatusForbidden, "AccessDenied", "Human verification requires a current session or scoped API credential")
	}
	var err error
	input.ProjectName, err = normalizeAssetProject(input.ProjectName)
	if err != nil {
		return empty, err
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 64 || utf8.RuneCountInString(input.Description) > 300 || input.ChannelID < 0 {
		return empty, assetError(http.StatusBadRequest, "InvalidParameter", "Enter a name of up to 64 characters and a description of up to 300 characters")
	}
	if len(input.IdempotencyKey) < 8 || len(input.IdempotencyKey) > 128 || strings.ContainsAny(input.IdempotencyKey, "\r\n\t ") {
		return empty, assetError(http.StatusBadRequest, "InvalidParameter.IdempotencyKey", "IdempotencyKey must contain 8 to 128 characters without spaces")
	}
	requestData, _ := common.Marshal([]any{actor.UserID, input})
	requestHash := assetValidationHash(string(requestData))
	idempotencyHash := assetValidationHash(input.IdempotencyKey)
	existing, err := model.GetAssetValidationByRequest(ctx, actor.InitiatorID, idempotencyHash)
	if err == nil {
		if existing.RequestHash != requestHash {
			return empty, model.ErrAssetConflict
		}
		if err = authorizeAssetValidation(ctx, actor, existing); err != nil {
			return empty, err
		}
		return assetValidationView(existing)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return empty, err
	}
	callbackBase, err := url.Parse(strings.TrimRight(system_setting.ServerAddress, "/"))
	if err != nil || callbackBase.Scheme != "https" || callbackBase.Host == "" || callbackBase.User != nil || callbackBase.RawQuery != "" || callbackBase.Fragment != "" || (callbackBase.Path != "" && callbackBase.Path != "/") {
		return empty, assetError(http.StatusServiceUnavailable, "ValidationConfigurationRequired", "Human verification requires an HTTPS server address without a path or query")
	}
	currentActor, err := AssetActorForUser(ctx, actor.UserID, actor.TokenID)
	if err != nil {
		return empty, err
	}
	if actor.TokenID > 0 {
		currentActor.ChannelIDs = slices.DeleteFunc(currentActor.ChannelIDs, func(id int) bool { return !slices.Contains(actor.ChannelIDs, id) })
	}
	var config system_setting.AssetChannelConfig
	var channel *model.Channel
	for _, id := range currentActor.ChannelIDs {
		if input.ChannelID != 0 && input.ChannelID != id {
			continue
		}
		config, err = model.LoadAssetChannelConfig(ctx, id)
		if err != nil {
			return empty, err
		}
		if config.AuthType != "aksk" || config.AccessKey == "" || config.SecretKey == "" {
			continue
		}
		channel, err = model.GetChannelById(id, true)
		if err != nil {
			return empty, err
		}
		break
	}
	if channel == nil {
		return empty, assetError(http.StatusForbidden, "NoAvailableChannel", "No AK/SK material channel is available for human verification")
	}
	stateBytes := make([]byte, 32)
	if _, err = rand.Read(stateBytes); err != nil {
		return empty, err
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	callbackBase.Path = assetValidationCallbackPath + state
	callbackURL := callbackBase.String()
	session := &model.AssetValidationSession{PublicID: "validation-na-" + common.GetUUID(), UserID: actor.UserID, InitiatorID: actor.InitiatorID,
		ProjectName: input.ProjectName, Name: input.Name, Description: input.Description, TokenID: actor.TokenID, AccessTokenID: actor.AccessTokenID,
		AuthSessionID: actor.AuthSessionID, UserAuthVersion: actor.UserAuthVersion, SessionVersion: actor.SessionVersion,
		ChannelID: channel.Id, ChannelScope: model.AssetChannelScope(channel), ChannelConfig: model.AssetValidationConfigHash(config), UpstreamProject: config.ProjectName,
		RateScope: assetValidationHash("ark-validation\x00" + config.AccessKey), StateHash: assetValidationHash(state),
		IdempotencyHash: idempotencyHash, RequestHash: requestHash, ExpiresAt: time.Now().Add(30 * time.Minute).Unix()}
	created, err := model.ReserveAssetValidationSession(ctx, session, func(tx *gorm.DB) error {
		return authorizeAssetValidationChannelWithDB(ctx, tx, session)
	})
	if err != nil {
		return empty, assetValidationError(err)
	}
	if !created {
		if err = authorizeAssetValidation(ctx, actor, session); err != nil {
			return empty, err
		}
		return assetValidationView(session)
	}
	var upstream struct {
		BytedToken  string `json:"BytedToken"`
		H5Link      string `json:"H5Link"`
		CallbackURL string `json:"CallbackURL"`
	}
	err = callAssetUpstream(ctx, channel, config, "CreateVisualValidateSession", map[string]any{"CallbackURL": callbackURL}, &upstream)
	if err == nil {
		h5, parseErr := url.Parse(upstream.H5Link)
		if upstream.BytedToken == "" || len(upstream.BytedToken) > 4096 || len(upstream.H5Link) > 8192 || parseErr != nil || h5.Scheme != "https" || h5.Host == "" || h5.User != nil ||
			(upstream.CallbackURL != "" && upstream.CallbackURL != callbackURL) {
			err = assetError(http.StatusBadGateway, "UpstreamResultUnknown", "The upstream verification response could not be confirmed")
		} else {
			session.SecretCipher, err = sealAssetValidationSecrets(session.PublicID, assetValidationSecrets{BytedToken: upstream.BytedToken, H5Link: upstream.H5Link, CallbackURL: callbackURL})
		}
	}
	if err == nil {
		session.Status = model.AssetValidationPending
	} else {
		session.Status = model.AssetValidationUnknown
		session.ErrorCode, session.ErrorMessage, _ = assetFailure(err)
		var failure *AssetError
		if errors.As(err, &failure) && failure.HTTPStatus >= 400 && failure.HTTPStatus < 500 && failure.HTTPStatus != http.StatusRequestTimeout && failure.Code != "UpstreamResultUnknown" {
			session.Status = model.AssetValidationFailed
			session.ResourcePossible = false
		}
		session.ErrorCode = safeAssetMessage(session.ErrorCode, state, upstream.BytedToken)
		session.ErrorMessage = safeAssetMessage(session.ErrorMessage, state, upstream.BytedToken)
	}
	// A disconnected browser must not discard a provider result that was already
	// received. This short detached write never performs a second remote call.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if saveErr := model.SaveAssetValidationAttempt(persistCtx, session); saveErr != nil {
		return empty, saveErr
	}
	if err == nil {
		if err = authorizeAssetValidation(ctx, actor, session); err != nil {
			return empty, err
		}
	}
	return assetValidationView(session)
}

func GetAssetValidationResult(ctx context.Context, actor dto.AssetActor, token, project string) (dto.AssetValidationView, error) {
	var empty dto.AssetValidationView
	project, err := normalizeAssetProject(project)
	if err != nil {
		return empty, err
	}
	if !strings.HasPrefix(token, "validation-na-") || len(token) > 64 {
		return empty, assetError(http.StatusBadRequest, "InvalidParameter.BytedToken", "Use the verification session returned by this gateway")
	}
	session, err := model.GetAssetValidationSession(ctx, actor.UserID, actor.InitiatorID, token, project)
	if err != nil {
		return empty, assetValidationError(err)
	}
	if err = authorizeAssetValidation(ctx, actor, session); err != nil {
		return empty, err
	}
	if session.Status == model.AssetValidationCreated {
		return assetValidationView(session)
	}
	if session.ExpiresAt <= time.Now().Unix() && session.UpstreamGroupID == "" {
		if err = model.ExpireAssetValidationSecrets(ctx, time.Now().Unix()); err != nil {
			return empty, err
		}
		session.SecretCipher = ""
		return assetValidationView(session)
	}
	claimed, err := model.ClaimAssetValidationResult(ctx, session)
	if err != nil {
		return empty, assetValidationError(err)
	}
	if !claimed {
		return assetValidationView(session)
	}
	defer func() {
		if session.LeaseToken == "" {
			return
		}
		// Loading configuration or decrypting a credential can fail before the
		// upstream call. Release that claim so a repaired configuration can be
		// retried immediately; a replaced lease still rejects this completion.
		session.Status = model.AssetValidationPending
		if session.UpstreamGroupID != "" {
			session.Status = model.AssetValidationUnknown
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = model.SaveAssetValidationAttempt(persistCtx, session)
	}()
	channel, err := model.GetChannelById(session.ChannelID, true)
	if err != nil {
		return empty, err
	}
	config, err := model.LoadAssetChannelConfig(ctx, session.ChannelID)
	if err != nil {
		return empty, err
	}
	secrets := assetValidationSecrets{}
	if session.UpstreamGroupID == "" {
		secrets, err = openAssetValidationSecrets(session)
		if err != nil {
			return empty, err
		}
		var result struct {
			GroupID string `json:"GroupId"`
		}
		err = callAssetUpstream(ctx, channel, config, "GetVisualValidateResult", map[string]any{"BytedToken": secrets.BytedToken}, &result)
		if err == nil && result.GroupID != "" && len(result.GroupID) <= 191 && strings.TrimSpace(result.GroupID) == result.GroupID {
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err = model.SaveAssetValidationGroupID(persistCtx, session, result.GroupID)
			cancel()
			if err != nil {
				return empty, err
			}
		} else if err == nil {
			err = assetError(http.StatusConflict, "ValidationPending", "Human verification has not completed")
		}
	}
	if err == nil {
		var group struct {
			ID          string `json:"Id"`
			GroupType   string `json:"GroupType"`
			ProjectName string `json:"ProjectName"`
		}
		err = callAssetUpstream(ctx, channel, config, "GetAssetGroup", map[string]any{"Id": session.UpstreamGroupID}, &group)
		if err == nil && (group.ID != session.UpstreamGroupID || group.GroupType != model.AssetGroupLivenessFace || group.ProjectName != session.UpstreamProject) {
			err = assetError(http.StatusBadGateway, "UpstreamInvalidResponse", "The upstream group does not match this human verification")
		}
	}
	if err == nil {
		// Permissions can be revoked while a remote request is in flight. Recheck
		// the live channel policy and then the original identity under row locks.
		err = authorizeAssetValidation(ctx, actor, session)
	}
	if err == nil {
		err = model.CompleteAssetValidationSession(ctx, session, func(tx *gorm.DB) error {
			return authorizeAssetValidationChannelWithDB(ctx, tx, session)
		})
	}
	if err != nil {
		session.Status = model.AssetValidationPending
		if session.UpstreamGroupID != "" {
			session.Status = model.AssetValidationUnknown
		}
		session.ErrorCode, session.ErrorMessage, _ = assetFailure(err)
		state := strings.TrimPrefix(secrets.CallbackURL, strings.TrimRight(system_setting.ServerAddress, "/")+assetValidationCallbackPath)
		session.ErrorCode = safeAssetMessage(session.ErrorCode, secrets.BytedToken, state)
		session.ErrorMessage = safeAssetMessage(session.ErrorMessage, secrets.BytedToken, state)
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		saveErr := model.SaveAssetValidationAttempt(persistCtx, session)
		cancel()
		if saveErr != nil {
			return empty, saveErr
		}
		// Do not disclose the H5 link after the original authorization was revoked.
		if permissionErr := authorizeAssetValidation(ctx, actor, session); permissionErr != nil {
			return empty, permissionErr
		}
	}
	return assetValidationView(session)
}

func ListAssetValidationSessions(ctx context.Context, actor dto.AssetActor, project string) ([]dto.AssetValidationSummary, error) {
	if actor.UserID <= 0 || actor.InitiatorID <= 0 || actor.AllUsers {
		return nil, assetError(http.StatusForbidden, "AccessDenied", "Select an asset owner before viewing verification sessions")
	}
	project, err := normalizeAssetProject(project)
	if err != nil {
		return nil, err
	}
	sessions, err := model.ListAssetValidationSessions(ctx, actor.UserID, actor.InitiatorID, project)
	if err != nil {
		return nil, err
	}
	items := make([]dto.AssetValidationSummary, 0, len(sessions))
	for _, session := range sessions {
		status := session.Status
		if status != model.AssetValidationCreated && session.UpstreamGroupID == "" && session.ExpiresAt <= time.Now().Unix() {
			status = model.AssetValidationExpired
		} else if status == model.AssetValidationCreating && session.LeaseExpiresAt <= time.Now().Unix() {
			status = model.AssetValidationUnknown
		}
		item := dto.AssetValidationSummary{SessionID: session.PublicID, Status: status, GroupID: session.GroupID, ExpiresAt: session.ExpiresAt, Name: session.Name, ChannelID: session.ChannelID}
		if session.ErrorCode != "" {
			item.Error = &dto.AssetFailure{Code: session.ErrorCode, Message: session.ErrorMessage}
		}
		items = append(items, item)
	}
	return items, nil
}

// ValidateAssetValidationCallback is deliberately read-only. Redirect parameters
// have no authority to create a group or to decide success or failure.
func ValidateAssetValidationCallback(ctx context.Context, state, upstreamToken string) error {
	invalid := assetError(http.StatusBadRequest, "InvalidValidationCallback", "This verification link is invalid or expired")
	stateBytes, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil || len(stateBytes) != 32 || upstreamToken == "" || len(upstreamToken) > 4096 {
		return invalid
	}
	session, err := model.GetAssetValidationByState(ctx, assetValidationHash(state))
	if err != nil || session.ExpiresAt <= time.Now().Unix() {
		return invalid
	}
	// The initiating page can finish polling before the phone follows its H5
	// redirect. Its secrets are already erased; a known, unexpired state still
	// gets the neutral return instruction, without asserting success or binding.
	if session.Status == model.AssetValidationCreated || session.UpstreamGroupID != "" {
		return nil
	}
	if session.SecretCipher == "" {
		return invalid
	}
	secrets, err := openAssetValidationSecrets(session)
	if err != nil || subtle.ConstantTimeCompare([]byte(secrets.BytedToken), []byte(upstreamToken)) != 1 {
		return invalid
	}
	return nil
}

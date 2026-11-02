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
		return assetError(http.StatusForbidden, "ValidationAuthorizationRevoked", "The original verification authorization is no longer valid")
	case errors.Is(err, model.ErrAssetValidationActive):
		return assetError(http.StatusConflict, "ValidationStillActive", "Waiting or checking verification records cannot be archived")
	case errors.Is(err, model.ErrAssetValidationReviewBatch):
		return assetError(http.StatusConflict, "ValidationReviewRequired", "Archive records with unconfirmed results individually")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return assetError(http.StatusNotFound, "ResourceNotFound", "The verification session was not found")
	default:
		return err
	}
}

func assetValidationActorFailure(actor dto.AssetActor, session *model.AssetValidationSession) *dto.AssetFailure {
	if actor.ReadOnly {
		return &dto.AssetFailure{Code: "ValidationReadOnly", Message: "Your current credential cannot change verification records"}
	}
	if actor.AllUsers || actor.UserID != session.UserID || actor.InitiatorID != session.InitiatorID {
		return &dto.AssetFailure{Code: "AccessDenied", Message: "The original verification authorization is no longer valid"}
	}
	if actor.TokenID != session.TokenID || actor.AccessTokenID != session.AccessTokenID {
		return &dto.AssetFailure{Code: "ValidationCredentialMismatch", Message: "Continue with the API credential that started this verification"}
	}
	if actor.AuthSessionID != session.AuthSessionID {
		return &dto.AssetFailure{Code: "ValidationSessionMismatch", Message: "Continue in the login session that started this verification"}
	}
	if actor.UserAuthVersion != session.UserAuthVersion || actor.SessionVersion != session.SessionVersion {
		return &dto.AssetFailure{Code: "ValidationAuthorizationRevoked", Message: "The original verification authorization is no longer valid"}
	}
	return nil
}

func authorizeAssetValidation(ctx context.Context, actor dto.AssetActor, session *model.AssetValidationSession) error {
	if failure := assetValidationActorFailure(actor, session); failure != nil {
		return assetError(http.StatusForbidden, failure.Code, failure.Message)
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
	err = model.ValidateAssetValidationSession(ctx, session)
	if errors.Is(err, model.ErrAssetConflict) {
		return assetError(http.StatusForbidden, "ValidationChannelChanged", "The verification channel configuration has changed")
	}
	return assetValidationError(err)
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

func assetValidationSummary(session *model.AssetValidationSession, actor dto.AssetActor, now int64) dto.AssetValidationSummary {
	view := dto.AssetValidationSummary{SessionID: session.PublicID, Status: session.Status, GroupID: session.GroupID, ExpiresAt: session.ExpiresAt,
		Name: session.Name, NextPollAt: session.NextPollAt, ProjectName: session.ProjectName,
		ArchivedAt: session.ArchivedAt, CreatedAt: session.CreatedAt, LastCheckedAt: session.LastCheckedAt}
	if actor.CanReadChannels {
		view.ChannelID = session.ChannelID
	}
	category := model.AssetValidationRecordCategory(session, now)
	view.NeedsReview = category == "needs_review"
	view.BindingPending = session.GroupID == "" && session.UpstreamGroupID != ""
	view.CanArchive = !actor.ReadOnly && (session.ArchivedAt > 0 || (category != "pending" && session.LeaseExpiresAt <= now))
	if session.ErrorCode != "" && session.ErrorCode != "ValidationPending" {
		view.Error = &dto.AssetFailure{Code: session.ErrorCode, Message: session.ErrorMessage}
		if !actor.CanReadChannels {
			switch {
			case session.ErrorCode == "Throttling":
				view.Error.Message = "Human verification is busy; retry shortly"
			case model.AssetValidationPollBlocked(session.ErrorCode):
				view.Error.Message = "Human verification cannot continue. Contact an administrator."
			case session.Status == model.AssetValidationFailed && session.SecretCipher == "" && session.UpstreamGroupID == "" && !session.ResourcePossible:
				view.Error = &dto.AssetFailure{Code: "ValidationCreateFailed", Message: "Human verification could not be started. Contact an administrator."}
			case session.SecretCipher != "" || session.UpstreamGroupID != "":
				view.Error = &dto.AssetFailure{Code: "ValidationCheckUnavailable", Message: "Unable to check verification right now. Try again later."}
			default:
				view.Error = &dto.AssetFailure{Code: "ValidationResultUnknown", Message: "The verification result could not be confirmed. Contact an administrator."}
			}
		}
	}
	if session.GroupID != "" {
		view.Status = model.AssetValidationCreated
		view.NextPollAt = 0
		return view
	}
	if session.ExpiresAt <= now && session.UpstreamGroupID == "" && session.LeaseExpiresAt <= now {
		view.Status = model.AssetValidationExpired
		return view
	}
	if session.Status == model.AssetValidationCreating && session.LeaseExpiresAt <= now {
		view.Status = model.AssetValidationUnknown
	}
	if session.Status == model.AssetValidationFailed && session.UpstreamGroupID == "" && session.LeaseExpiresAt <= now {
		return view
	}
	// A saved group can still be bound after the H5 credential expires. An
	// uncertain create without a credential cannot safely be submitted again.
	view.CanContinue = session.UpstreamGroupID != "" || session.LeaseExpiresAt > now || (session.SecretCipher != "" && session.ExpiresAt > now)
	view.CanPoll = view.CanContinue && (session.LeaseExpiresAt > now || !model.AssetValidationPollBlocked(session.ErrorCode))
	if view.CanContinue {
		view.ContinueError = assetValidationActorFailure(actor, session)
		if view.ContinueError != nil {
			view.CanContinue, view.CanPoll = false, false
		}
	}
	return view
}

func assetValidationView(session *model.AssetValidationSession, actor dto.AssetActor) (dto.AssetValidationView, error) {
	summary := assetValidationSummary(session, actor, time.Now().Unix())
	view := dto.AssetValidationView{BytedToken: session.PublicID, Status: summary.Status, GroupID: summary.GroupID, ExpiresAt: summary.ExpiresAt,
		Name: summary.Name, ChannelID: summary.ChannelID, CanPoll: summary.CanPoll, NextPollAt: summary.NextPollAt, Error: summary.Error,
		CanContinue: summary.CanContinue, ContinueError: summary.ContinueError, NeedsReview: summary.NeedsReview, BindingPending: summary.BindingPending,
		CanArchive: summary.CanArchive, ArchivedAt: summary.ArchivedAt, CreatedAt: summary.CreatedAt, LastCheckedAt: summary.LastCheckedAt, ProjectName: summary.ProjectName}
	if session.SecretCipher != "" && session.ExpiresAt > time.Now().Unix() {
		secrets, err := openAssetValidationSecrets(session)
		if err != nil {
			code, message, _ := assetFailure(err)
			if !actor.CanReadChannels {
				message = "Human verification cannot continue. Contact an administrator."
			}
			view.Error = &dto.AssetFailure{Code: code, Message: message}
			view.CanPoll, view.CanContinue = false, false
			view.NeedsReview = session.ResourcePossible && session.UpstreamGroupID == ""
			return view, nil
		}
		view.H5Link, view.CallbackURL = secrets.H5Link, secrets.CallbackURL
	}
	return view, nil
}

func CreateAssetValidationSession(ctx context.Context, actor dto.AssetActor, input dto.AssetValidationCreate) (dto.AssetValidationView, error) {
	var empty dto.AssetValidationView
	if actor.ReadOnly || actor.UserID <= 0 || actor.InitiatorID <= 0 || actor.AllUsers || (actor.TokenID == 0 && actor.AuthSessionID == "") {
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
		return assetValidationView(existing, actor)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return empty, err
	}
	// Replaying a fully matched old request above remains valid even if the
	// caller could choose a channel before manual selection became restricted.
	if input.ChannelID != 0 && !actor.CanReadChannels {
		return empty, assetError(http.StatusForbidden, "AccessDenied", "You do not have permission to select a verification channel.")
	}
	callbackBase, err := url.Parse(strings.TrimRight(system_setting.ServerAddress, "/"))
	if err != nil || (callbackBase.Scheme != "http" && callbackBase.Scheme != "https") || callbackBase.Host == "" || callbackBase.User != nil || callbackBase.RawQuery != "" || callbackBase.Fragment != "" || (callbackBase.Path != "" && callbackBase.Path != "/") {
		message := "Human verification is not configured correctly. Contact an administrator."
		if actor.CanReadChannels {
			message = "Human verification requires an HTTP or HTTPS server address without a path or query"
		}
		return empty, assetError(http.StatusServiceUnavailable, "ValidationConfigurationRequired", message)
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
		validAuth := config.AuthType == "bearer" || (config.AuthType == "aksk" && config.AccessKey != "" && config.SecretKey != "")
		if !validAuth {
			continue
		}
		channel, err = model.GetChannelById(id, true)
		if err != nil {
			return empty, err
		}
		break
	}
	if channel == nil {
		message := "Human verification is not available. Contact an administrator."
		if actor.CanReadChannels {
			message = "No material channel is available for human verification"
			if input.ChannelID != 0 {
				return empty, assetError(http.StatusForbidden, "ValidationChannelUnavailable", "The selected channel is unavailable for human verification.")
			}
		}
		return empty, assetError(http.StatusForbidden, "NoAvailableChannel", message)
	}
	// Keep the AK scope compatible with sessions and rate windows already stored.
	rateIdentity := "ark-validation\x00" + config.AccessKey
	if config.AuthType == "bearer" {
		endpoint, parseErr := url.Parse(config.Endpoint)
		if parseErr != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
			message := "Human verification is not configured correctly. Contact an administrator."
			if actor.CanReadChannels {
				message = "The material endpoint is invalid"
			}
			return empty, assetError(http.StatusBadGateway, "UpstreamConfigurationInvalid", message)
		}
		endpoint.Host = strings.TrimSuffix(strings.ToLower(endpoint.Host), ":443")
		// Equivalent endpoint spellings share a gate; different providers and
		// paths may issue identical keys and must not block each other.
		rateIdentity = "ark-validation-bearer\x00" + strings.TrimRight(endpoint.String(), "/") + "\x00" + channel.Key
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
		RateScope: assetValidationHash(rateIdentity), StateHash: assetValidationHash(state),
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
		return assetValidationView(session, actor)
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
		if errors.As(err, &failure) {
			if failure.HTTPStatus >= 400 && failure.HTTPStatus < 500 && failure.HTTPStatus != http.StatusRequestTimeout && failure.Code != "UpstreamResultUnknown" {
				session.Status = model.AssetValidationFailed
				session.ResourcePossible = false
			}
			if failure.HTTPStatus == http.StatusTooManyRequests {
				session.ErrorCode = "Throttling"
				session.NextPollAt = failure.RetryAfter
			}
		}
		session.ErrorCode = safeAssetMessage(session.ErrorCode, state, upstream.BytedToken)
		session.ErrorMessage = safeAssetMessage(session.ErrorMessage, state, upstream.BytedToken)
	}
	// A disconnected browser must not discard a provider result that was already
	// received. This short detached write never performs a second remote call.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if saveErr := model.SaveAssetValidationAttempt(persistCtx, session, 0); saveErr != nil {
		return empty, saveErr
	}
	if err == nil {
		if err = authorizeAssetValidation(ctx, actor, session); err != nil {
			return empty, err
		}
	}
	return assetValidationView(session, actor)
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
		return assetValidationView(session, actor)
	}
	if session.ExpiresAt <= time.Now().Unix() && session.UpstreamGroupID == "" && session.LeaseExpiresAt <= time.Now().Unix() {
		if err = model.ExpireAssetValidationSecrets(ctx, time.Now().Unix()); err != nil {
			return empty, err
		}
		session.SecretCipher = ""
		return assetValidationView(session, actor)
	}
	claimed, err := model.ClaimAssetValidationResult(ctx, session)
	if err != nil {
		return empty, assetValidationError(err)
	}
	if !claimed {
		return assetValidationView(session, actor)
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
		_ = model.SaveAssetValidationAttempt(persistCtx, session, 0)
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
	pending := false
	if session.UpstreamGroupID == "" {
		secrets, err = openAssetValidationSecrets(session)
		var result struct {
			GroupID string `json:"GroupId"`
		}
		if err == nil {
			err = callAssetUpstream(ctx, channel, config, "GetVisualValidateResult", map[string]any{"BytedToken": secrets.BytedToken}, &result)
		}
		if err == nil && result.GroupID != "" && len(result.GroupID) <= 191 && strings.TrimSpace(result.GroupID) == result.GroupID {
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err = model.SaveAssetValidationGroupID(persistCtx, session, result.GroupID)
			cancel()
			if err != nil {
				return empty, err
			}
		} else if err == nil && result.GroupID == "" {
			pending = true
		} else if err == nil {
			err = assetError(http.StatusBadGateway, "InvalidValidationGroup", "The upstream verification returned an invalid group ID")
		}
	}
	if err == nil && !pending {
		var group struct {
			ID          string `json:"Id"`
			GroupType   string `json:"GroupType"`
			ProjectName string `json:"ProjectName"`
		}
		err = callAssetUpstream(ctx, channel, config, "GetAssetGroup", map[string]any{"Id": session.UpstreamGroupID}, &group)
		if err == nil && (group.ID != session.UpstreamGroupID || group.GroupType != model.AssetGroupLivenessFace || group.ProjectName != session.UpstreamProject) {
			err = assetError(http.StatusBadGateway, "ValidationGroupMismatch", "The upstream group does not match this human verification")
		}
	}
	if err == nil {
		// Permissions can be revoked while a remote request is in flight. Recheck
		// the live channel policy and then the original identity under row locks.
		err = authorizeAssetValidation(ctx, actor, session)
	}
	if err == nil && !pending {
		err = model.CompleteAssetValidationSession(ctx, session, func(tx *gorm.DB) error {
			return authorizeAssetValidationChannelWithDB(ctx, tx, session)
		})
	}
	if err != nil || pending {
		session.Status = model.AssetValidationPending
		if session.UpstreamGroupID != "" {
			session.Status = model.AssetValidationUnknown
		}
		delay := int64(10)
		if session.ErrorCode != "" && session.ErrorCode != "ValidationPending" {
			delay = max(10, min(60, 2*(session.NextPollAt-session.UpdatedAt)))
		}
		session.NextPollAt = 0
		session.ErrorCode, session.ErrorMessage = "", ""
		minimumDelay := int64(0)
		if err != nil {
			var retryable bool
			session.ErrorCode, session.ErrorMessage, retryable = assetFailure(err)
			var failure *AssetError
			if errors.As(err, &failure) {
				// Before H5 completion, providers may return business errors
				// instead of a waiting result. They do not prove a final failure.
				retryable = retryable || (failure.HTTPStatus >= 400 && failure.HTTPStatus < 500)
				session.NextPollAt = failure.RetryAfter
				if failure.HTTPStatus == http.StatusTooManyRequests {
					session.ErrorCode = "Throttling"
				}
			}
			if retryable {
				minimumDelay = delay
			}
		}
		state := ""
		if callback, parseErr := url.Parse(secrets.CallbackURL); parseErr == nil {
			state = strings.TrimPrefix(callback.Path, assetValidationCallbackPath)
		}
		session.ErrorCode = safeAssetMessage(session.ErrorCode, secrets.BytedToken, state)
		session.ErrorMessage = safeAssetMessage(session.ErrorMessage, secrets.BytedToken, state)
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		saveErr := model.SaveAssetValidationAttempt(persistCtx, session, minimumDelay)
		cancel()
		if saveErr != nil {
			return empty, saveErr
		}
		// Do not disclose the H5 link after the original authorization was revoked.
		if permissionErr := authorizeAssetValidation(ctx, actor, session); permissionErr != nil {
			return empty, permissionErr
		}
	}
	return assetValidationView(session, actor)
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
	return assetValidationSummaries(ctx, actor, sessions, time.Now().Unix()), nil
}

// History never decrypts an H5 credential, contacts an upstream, or invokes the
// transactional authorization path. These capabilities are hints; mutations
// still validate the original authorization and channel under locks.
func assetValidationSummaries(ctx context.Context, actor dto.AssetActor, sessions []model.AssetValidationSession, now int64) []dto.AssetValidationSummary {
	items := make([]dto.AssetValidationSummary, 0, len(sessions))
	var currentActor dto.AssetActor
	var permissionErr error
	permissionsLoaded := false
	channels := make(map[int]*model.Channel)
	configs := make(map[int]system_setting.AssetChannelConfig)
	for _, session := range sessions {
		view := assetValidationSummary(&session, actor, now)
		channel, loaded := channels[session.ChannelID]
		if !loaded {
			channel, _ = model.GetChannelById(session.ChannelID, true)
			channels[session.ChannelID] = channel
			config, _ := model.LoadAssetChannelConfig(ctx, session.ChannelID)
			configs[session.ChannelID] = config
		}
		if actor.CanReadChannels && channel != nil {
			view.ChannelName = channel.Name
		}
		if view.CanContinue {
			if !permissionsLoaded {
				currentActor, permissionErr = AssetActorForUser(ctx, actor.UserID, actor.TokenID)
				permissionsLoaded = true
			}
			if permissionErr != nil || !slices.Contains(currentActor.ChannelIDs, session.ChannelID) ||
				(actor.TokenID > 0 && !slices.Contains(actor.ChannelIDs, session.ChannelID)) {
				view.ContinueError = &dto.AssetFailure{Code: "ValidationChannelUnavailable", Message: "The verification channel is unavailable to this caller"}
			} else if config := configs[session.ChannelID]; channel == nil || model.AssetChannelScope(channel) != session.ChannelScope ||
				model.AssetValidationConfigHash(config) != session.ChannelConfig || config.ProjectName != session.UpstreamProject {
				view.ContinueError = &dto.AssetFailure{Code: "ValidationChannelChanged", Message: "The verification channel configuration has changed"}
			}
			if view.ContinueError != nil {
				view.CanContinue, view.CanPoll = false, false
			}
		}
		items = append(items, view)
	}
	return items
}

func ListAssetValidationRecords(ctx context.Context, actor dto.AssetActor, project string, filter dto.AssetValidationRecordFilter) (dto.AssetValidationRecords, error) {
	var result dto.AssetValidationRecords
	if actor.UserID <= 0 || actor.InitiatorID <= 0 || actor.AllUsers {
		return result, assetError(http.StatusForbidden, "AccessDenied", "Select an asset owner before viewing verification sessions")
	}
	project, err := normalizeAssetProject(project)
	if err != nil {
		return result, err
	}
	if filter.Page == 0 {
		filter.Page = 1
	}
	if filter.PageSize == 0 {
		filter.PageSize = 20
	}
	if filter.Status == "" {
		filter.Status = "all"
	}
	if filter.Archived == "" {
		filter.Archived = "active"
	}
	filter.Name = strings.TrimSpace(filter.Name)
	if filter.Page < 1 || filter.Page > 100000 || filter.PageSize < 1 || filter.PageSize > 50 || utf8.RuneCountInString(filter.Name) > 64 || strings.ContainsRune(filter.Name, '\x00') ||
		!slices.Contains([]string{"all", "pending", "completed", "needs_review", "ended"}, filter.Status) ||
		!slices.Contains([]string{"active", "archived", "all"}, filter.Archived) {
		return result, assetError(http.StatusBadRequest, "InvalidParameter", "Invalid verification record filters")
	}
	now := time.Now().Unix()
	sessions, total, pending, err := model.ListAssetValidationRecords(ctx, actor.UserID, actor.InitiatorID, project, filter, now)
	if err != nil {
		return result, err
	}
	result.Items = assetValidationSummaries(ctx, actor, sessions, now)
	result.TotalCount, result.PendingCount = total, pending
	result.Page, result.PageSize = filter.Page, filter.PageSize
	return result, nil
}

func GetAssetValidationRecord(ctx context.Context, actor dto.AssetActor, id, project string) (dto.AssetValidationSummary, error) {
	var empty dto.AssetValidationSummary
	if actor.UserID <= 0 || actor.InitiatorID <= 0 || actor.AllUsers {
		return empty, assetError(http.StatusForbidden, "AccessDenied", "Select an asset owner before viewing verification sessions")
	}
	if !strings.HasPrefix(id, "validation-na-") || len(id) > 64 {
		return empty, assetError(http.StatusBadRequest, "InvalidParameter", "Use the verification session returned by this gateway")
	}
	project, err := normalizeAssetProject(project)
	if err != nil {
		return empty, err
	}
	session, err := model.GetAssetValidationSession(ctx, actor.UserID, actor.InitiatorID, id, project)
	if err != nil {
		return empty, assetValidationError(err)
	}
	return assetValidationSummaries(ctx, actor, []model.AssetValidationSession{*session}, time.Now().Unix())[0], nil
}

func ArchiveAssetValidationRecords(ctx context.Context, actor dto.AssetActor, input dto.AssetValidationArchive) error {
	if actor.ReadOnly || actor.AllUsers || actor.UserID <= 0 || actor.InitiatorID <= 0 {
		return assetError(http.StatusForbidden, "AccessDenied", "Your current credential cannot change verification records")
	}
	project, err := normalizeAssetProject(input.ProjectName)
	if err != nil {
		return err
	}
	if input.Archived == nil || len(input.SessionIDs) == 0 || len(input.SessionIDs) > 50 {
		return assetError(http.StatusBadRequest, "InvalidParameter", "Select between 1 and 50 verification records")
	}
	ids := slices.Clone(input.SessionIDs)
	for _, id := range ids {
		if !strings.HasPrefix(id, "validation-na-") || len(id) > 64 {
			return assetError(http.StatusBadRequest, "InvalidParameter", "Use the verification session returned by this gateway")
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return assetValidationError(model.ArchiveAssetValidationRecords(ctx, actor.UserID, actor.InitiatorID, project, ids, *input.Archived))
}

func ListAssetValidationChannels(ctx context.Context, actor dto.AssetActor) ([]dto.AssetValidationChannel, error) {
	if !actor.CanReadChannels {
		return nil, assetError(http.StatusForbidden, "AccessDenied", "You do not have permission to view verification channels.")
	}
	if actor.UserID <= 0 || actor.InitiatorID <= 0 || actor.AllUsers {
		return nil, assetError(http.StatusForbidden, "AccessDenied", "Select an asset owner before viewing verification sessions")
	}
	currentActor, err := AssetActorForUser(ctx, actor.UserID, actor.TokenID)
	if err != nil {
		return nil, err
	}
	items := []dto.AssetValidationChannel{}
	for _, id := range currentActor.ChannelIDs {
		if actor.TokenID > 0 && !slices.Contains(actor.ChannelIDs, id) {
			continue
		}
		config, err := model.LoadAssetChannelConfig(ctx, id)
		if err != nil {
			return nil, err
		}
		if config.AuthType != "bearer" && (config.AuthType != "aksk" || config.AccessKey == "" || config.SecretKey == "") {
			continue
		}
		channel, err := model.GetChannelById(id, false)
		if err != nil {
			return nil, err
		}
		items = append(items, dto.AssetValidationChannel{ID: id, Name: channel.Name})
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

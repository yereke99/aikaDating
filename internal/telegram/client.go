package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"aika/internal/database"
	"aika/internal/domain"

	"go.uber.org/zap"
)

var ErrRecipientUnavailable = errors.New("recipient is unavailable")

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)

type apiError struct {
	StatusCode  int
	ErrorCode   int
	Description string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Telegram Bot API error %d", e.ErrorCode)
}

type Client struct {
	baseURL     string
	miniAppURL  string
	botUsername string
	httpClient  *http.Client
	store       *database.Store
	logger      *zap.Logger
}

func NewClient(token, miniAppURL, botUsername string, store *database.Store, logger *zap.Logger) *Client {
	return &Client{
		baseURL:     "https://api.telegram.org/bot" + token + "/",
		miniAppURL:  miniAppURL,
		botUsername: botUsername,
		httpClient:  &http.Client{Timeout: 40 * time.Second},
		store:       store,
		logger:      logger,
	}
}

type inlineKeyboardMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type inlineKeyboardButton struct {
	Text   string  `json:"text"`
	URL    string  `json:"url,omitempty"`
	WebApp *webApp `json:"web_app,omitempty"`
}

type webApp struct {
	URL string `json:"url"`
}

func (c *Client) SendLike(ctx context.Context, recipient, sender domain.User, message string) error {
	if !recipient.TelegramChatID.Valid {
		return ErrRecipientUnavailable
	}
	text := FormatLikeNotification(recipient.AppLanguage, sender, message, time.Now())
	buttons := []inlineKeyboardButton{{
		Text: profileButtonText(recipient.AppLanguage),
		URL:  fmt.Sprintf("https://t.me/%s?startapp=profile_%s", c.botUsername, sender.ID),
	}}
	if sender.Username.Valid && usernamePattern.MatchString(sender.Username.String) {
		buttons = append(buttons, inlineKeyboardButton{
			Text: telegramButtonText(recipient.AppLanguage),
			URL:  "https://t.me/" + sender.Username.String,
		})
	}
	markup := inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{buttons}}

	var err error
	if photoURL := sender.EffectivePhotoURL(); photoURL != "" {
		if strings.HasPrefix(photoURL, "/") {
			photoURL = strings.TrimRight(c.miniAppURL, "/") + photoURL
		}
		err = c.call(ctx, "sendPhoto", map[string]any{
			"chat_id":      recipient.TelegramChatID.Int64,
			"photo":        photoURL,
			"caption":      text,
			"parse_mode":   "HTML",
			"reply_markup": markup,
		}, nil)
		var telegramErr *apiError
		if errors.As(err, &telegramErr) && telegramErr.StatusCode == http.StatusBadRequest {
			_, err = c.sendText(ctx, recipient.TelegramChatID.Int64, text, markup)
		}
	} else {
		_, err = c.sendText(ctx, recipient.TelegramChatID.Int64, text, markup)
	}
	if isRecipientError(err) {
		return ErrRecipientUnavailable
	}
	return err
}

// SendCallInvite tells someone that a call is ringing for them right now.
//
// It is sent only when the recipient does not have the Mini App open, because a client that is
// already listening receives the invitation over the signalling channel instead. The button is a
// `startapp` deep link rather than a plain web_app button: that is the form Telegram opens as the
// Main Mini App, which is what restores the app's own fullscreen presentation. The call ID travels
// in the start parameter so the app can go straight to the ringing screen.
func (c *Client) SendCallInvite(ctx context.Context, recipient, caller domain.User, callID string) (int64, error) {
	if !recipient.TelegramChatID.Valid {
		return 0, ErrRecipientUnavailable
	}
	markup := inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{{{
		Text: answerButtonText(recipient.AppLanguage),
		// `call_` plus a UUID is 41 characters of [A-Za-z0-9_-], inside Telegram's start-parameter
		// limits, so the ID survives the round trip unchanged.
		URL: fmt.Sprintf("https://t.me/%s?startapp=call_%s", c.botUsername, callID),
	}}}}
	messageID, err := c.sendText(ctx, recipient.TelegramChatID.Int64, FormatCallNotification(recipient.AppLanguage, caller), markup)
	if isRecipientError(err) {
		return 0, ErrRecipientUnavailable
	}
	return messageID, err
}

// FormatCallNotification is the ringing message, in the recipient's own app language.
func FormatCallNotification(language string, caller domain.User) string {
	name := html.EscapeString(caller.Name())
	switch domain.NormalizeLanguage(language) {
	case domain.LanguageKazakh:
		return "📹 <b>" + name + "</b> сізге бейнеқоңырау шалып жатыр.\n\nЖауап беру үшін төмендегі батырманы басыңыз."
	case domain.LanguageEnglish:
		return "📹 <b>" + name + "</b> is calling you.\n\nTap the button below to answer."
	default:
		return "📹 <b>" + name + "</b> звонит вам.\n\nНажмите кнопку ниже, чтобы ответить."
	}
}

func answerButtonText(language string) string {
	switch domain.NormalizeLanguage(language) {
	case domain.LanguageKazakh:
		return "📹 Жауап беру"
	case domain.LanguageEnglish:
		return "📹 Answer"
	default:
		return "📹 Ответить"
	}
}

func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	if chatID == 0 || messageID == 0 {
		return nil
	}
	err := c.call(ctx, "deleteMessage", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}, nil)
	if isRecipientError(err) {
		return ErrRecipientUnavailable
	}
	return err
}

func (c *Client) sendText(ctx context.Context, chatID int64, text string, markup inlineKeyboardMarkup) (int64, error) {
	var sent message
	err := c.call(ctx, "sendMessage", map[string]any{
		"chat_id":      chatID,
		"text":         text,
		"parse_mode":   "HTML",
		"reply_markup": markup,
	}, &sent)
	return sent.MessageID, err
}

func isRecipientError(err error) bool {
	var telegramErr *apiError
	if !errors.As(err, &telegramErr) {
		return false
	}
	return telegramErr.StatusCode == http.StatusForbidden ||
		strings.Contains(strings.ToLower(telegramErr.Description), "chat not found") ||
		strings.Contains(strings.ToLower(telegramErr.Description), "user is deactivated")
}

func FormatLikeNotification(language string, sender domain.User, message string, now time.Time) string {
	name := html.EscapeString(sender.Name())
	age := sender.Age(now)
	username := ""
	if sender.Username.Valid && usernamePattern.MatchString(sender.Username.String) {
		username = "@" + html.EscapeString(sender.Username.String)
	}
	message = html.EscapeString(strings.TrimSpace(message))

	var title, nameLabel, ageLabel, telegramLabel, messageLabel string
	switch domain.NormalizeLanguage(language) {
	case domain.LanguageKazakh:
		title, nameLabel, ageLabel, telegramLabel, messageLabel = "❤️ Сізге қолданушы лайк қойды", "Аты", "Жасы", "Telegram", "Хабарлама"
	case domain.LanguageEnglish:
		title, nameLabel, ageLabel, telegramLabel, messageLabel = "❤️ A user liked your profile", "Name", "Age", "Telegram", "Message"
	default:
		title, nameLabel, ageLabel, telegramLabel, messageLabel = "❤️ Вас лайкнул пользователь", "Имя", "Возраст", "Telegram", "Сообщение"
	}
	lines := []string{title, "", "<b>" + nameLabel + ":</b> " + name}
	if age > 0 {
		lines = append(lines, fmt.Sprintf("<b>%s:</b> %d", ageLabel, age))
	}
	if username != "" {
		lines = append(lines, "<b>"+telegramLabel+":</b> "+username)
	}
	if message != "" {
		lines = append(lines, "", "<b>"+messageLabel+":</b>", message)
	}
	return strings.Join(lines, "\n")
}

func profileButtonText(language string) string {
	switch domain.NormalizeLanguage(language) {
	case domain.LanguageKazakh:
		return "Профильді ашу"
	case domain.LanguageEnglish:
		return "Open profile"
	default:
		return "Открыть профиль"
	}
}

func telegramButtonText(language string) string {
	switch domain.NormalizeLanguage(language) {
	case domain.LanguageKazakh:
		return "Telegram-да ашу"
	case domain.LanguageEnglish:
		return "Open in Telegram"
	default:
		return "Открыть в Telegram"
	}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func (c *Client) call(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("Telegram %s request: %w", method, err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 2<<20)
	var envelope apiResponse
	if err := json.NewDecoder(limited).Decode(&envelope); err != nil {
		return fmt.Errorf("decode Telegram %s response: %w", method, err)
	}
	if !envelope.OK {
		return &apiError{StatusCode: response.StatusCode, ErrorCode: envelope.ErrorCode, Description: envelope.Description}
	}
	if result != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("decode Telegram %s result: %w", method, err)
		}
	}
	return nil
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

type message struct {
	MessageID int64         `json:"message_id"`
	From      *telegramUser `json:"from"`
	Chat      chat          `json:"chat"`
	Text      string        `json:"text"`
}

type telegramUser struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

func (c *Client) Run(ctx context.Context) {
	if err := c.configure(ctx); err != nil && ctx.Err() == nil {
		c.logger.Warn("Telegram bot setup failed", zap.Error(err))
	}
	var offset int64
	for ctx.Err() == nil {
		var updates []update
		err := c.call(ctx, "getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         30,
			"allowed_updates": []string{"message"},
		}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logger.Warn("Telegram update polling failed", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for _, item := range updates {
			if item.UpdateID >= offset {
				offset = item.UpdateID + 1
			}
			c.handleUpdate(ctx, item)
		}
	}
}

func (c *Client) handleUpdate(ctx context.Context, item update) {
	if item.Message == nil || item.Message.From == nil || item.Message.Chat.Type != "private" {
		return
	}
	fields := strings.Fields(item.Message.Text)
	if len(fields) == 0 {
		return
	}
	command := strings.ToLower(strings.Split(fields[0], "@")[0])
	if command != "/start" && command != "/help" {
		return
	}
	from := item.Message.From
	profile, err := c.store.UpsertTelegramUser(ctx, domain.TelegramProfile{
		UserID:       from.ID,
		ChatID:       sql.NullInt64{Int64: item.Message.Chat.ID, Valid: true},
		Username:     from.Username,
		FirstName:    from.FirstName,
		LastName:     from.LastName,
		LanguageCode: from.LanguageCode,
	})
	if err != nil {
		c.logger.Error("Could not register bot chat", zap.Error(err))
		return
	}
	language := profile.AppLanguage
	markup := inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{{{
		Text:   openButtonText(language),
		WebApp: &webApp{URL: c.miniAppURL},
	}}}}
	if _, err := c.sendText(ctx, item.Message.Chat.ID, welcomeText(language), markup); err != nil && !isRecipientError(err) {
		c.logger.Warn("Could not send bot welcome", zap.Error(err))
	}
	_ = c.setChatMenu(ctx, item.Message.Chat.ID, language)
}

func (c *Client) configure(ctx context.Context) error {
	if err := c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil); err != nil {
		return err
	}
	for _, language := range []string{"", "ru", "kk", "en"} {
		payload := map[string]any{
			"commands": []map[string]string{{"command": "start", "description": commandDescription(language)}},
			"scope":    map[string]string{"type": "all_private_chats"},
		}
		if language != "" {
			payload["language_code"] = language
		}
		if err := c.call(ctx, "setMyCommands", payload, nil); err != nil {
			return err
		}
	}
	return c.call(ctx, "setChatMenuButton", map[string]any{
		"menu_button": map[string]any{
			"type":    "web_app",
			"text":    "AikaBot",
			"web_app": map[string]string{"url": c.miniAppURL},
		},
	}, nil)
}

func (c *Client) setChatMenu(ctx context.Context, chatID int64, language string) error {
	return c.call(ctx, "setChatMenuButton", map[string]any{
		"chat_id": chatID,
		"menu_button": map[string]any{
			"type":    "web_app",
			"text":    openButtonText(language),
			"web_app": map[string]string{"url": c.miniAppURL},
		},
	}, nil)
}

func welcomeText(language string) string {
	switch domain.NormalizeLanguage(language) {
	case domain.LanguageKazakh:
		return "AikaBot-қа қош келдіңіз.\n\nМұнда жақын жердегі адамдарды тауып, профильдерді көріп, лайк жібере аласыз.\n\nҚолданбаны ашу үшін төмендегі батырманы басыңыз."
	case domain.LanguageEnglish:
		return "Welcome to AikaBot.\n\nFind people nearby, view profiles, and send likes.\n\nTap the button below to open the app."
	default:
		return "Добро пожаловать в AikaBot.\n\nЗдесь вы можете находить людей рядом, просматривать анкеты и отправлять лайки.\n\nНажмите кнопку ниже, чтобы открыть приложение."
	}
}

func openButtonText(language string) string {
	switch domain.NormalizeLanguage(language) {
	case domain.LanguageKazakh:
		return "AikaBot-ты ашу"
	case domain.LanguageEnglish:
		return "Open AikaBot"
	default:
		return "Открыть AikaBot"
	}
}

func commandDescription(language string) string {
	switch language {
	case "kk":
		return "AikaBot-ты ашу"
	case "en":
		return "Open AikaBot"
	default:
		return "Открыть AikaBot"
	}
}

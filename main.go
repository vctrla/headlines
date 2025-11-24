package main

import (
	"context"
	"fmt"
	"html"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"headlines/dynamo"
	"headlines/feeds"
	"headlines/target"
	"headlines/tools"
	"headlines/typesPkg"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var allowedHours = []int{9, 19}

func shouldRunNow() bool {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		logger.Error("Failed to load Europe/Madrid timezone", zap.Error(err))
		return false
	}

	hour := time.Now().In(loc).Hour()
	return slices.Contains(allowedHours, hour)
}

// *
// **
// ***
// ****
// ***** logger
var logger *zap.Logger

func setupLogger() *zap.Logger {
	var core zapcore.Core
	var options []zap.Option

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.LevelKey = "level"
	encoderConfig.MessageKey = "message"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder

	core = zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(os.Stdout),
		zap.InfoLevel,
	)

	options = append(options, zap.AddCaller())

	return zap.New(core, options...)
}

func init() {
	logger = setupLogger()
}

// *
// **
// ***
// ****
// ***** collect
func collectUnpublished(
	ctx context.Context,
	articles []typesPkg.MainStruct,
	db *dynamodb.Client,
	envTarget string,
) ([]typesPkg.MainStruct, error) {
	toPublish := make([]typesPkg.MainStruct, 0, len(articles))
	for _, art := range articles {
		pub, err := dynamo.IsArticlePublished(ctx, db, art.GUID)
		if err != nil {
			logger.Error("is-published check failed", zap.Error(err), zap.String("guid", art.GUID))
			continue
		}
		if pub {
			continue
		}

		if envTarget == "email" {
			if art.Title == "" {
				// skip empty titles
			} else {
				// --- begin inlined FormatPost logic ---
				emojis := strings.TrimSpace(tools.GetEmojis(art.Title))
				header := html.EscapeString(art.Header)
				title := html.EscapeString(art.Title)
				link := html.EscapeString(art.Link)

				var parts []string
				if emojis != "" {
					parts = append(parts, emojis)
				}
				if header != "" {
					parts = append(parts, fmt.Sprintf("<b>%s</b>:", header))
				}
				parts = append(parts, title)
				display := strings.Join(parts, " ")

				li := fmt.Sprintf(
					`<p class="item" style="color:#000000;margin:0;">
						<a href="%s" style="color:#000000;text-decoration:none;">%s</a>
					</p>`,
					link, display,
				)

				toPublish = append(toPublish, typesPkg.MainStruct{
					GUID:  art.GUID,
					Title: li,
				})
			}
		} else {
			toPublish = append(toPublish, art)
		}

	}
	return toPublish, nil
}

// *
// **
// ***
// ****
// ***** main
type feedResult struct {
	Articles []typesPkg.MainStruct
	Err      error
}

func buildEmailHTML(items []string, subject string) string {
	sep := `<hr class="separator" style="border:none;border-top:2px dashed #ccc;margin:16px 0;">`
	bodyContent := sep + strings.Join(items, sep) + sep

	return fmt.Sprintf(`<!DOCTYPE html>
		<html>
		<head>
			<meta charset="utf-8">
			<meta name="viewport" content="width=device-width, initial-scale=1.0">
			<title>%s</title>
		</head>
		<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Arial,Helvetica,sans-serif;font-size:18px;line-height:1.4;">
			%s
		</body>
		</html>`,
		subject, bodyContent)
}

func runParsers(ctx context.Context, db *dynamodb.Client) error {
	envTarget := os.Getenv("TARGET")
	if envTarget == "" {
		return fmt.Errorf("TARGET not set")
	}
	email := os.Getenv("MAIN_EMAIL")
	if email == "" {
		return fmt.Errorf("MAIN_EMAIL not set")
	}
	telegramBot := os.Getenv("TELEGRAM_BOT")
	if telegramBot == "" {
		return fmt.Errorf("TELEGRAM_BOT not set")
	}
	telegramChannel := os.Getenv("TELEGRAM_CHANNEL")
	if telegramChannel == "" {
		return fmt.Errorf("TELEGRAM_CHANNEL not set")
	}
	smtpHost := os.Getenv("SMTP_HOST")
	if smtpHost == "" {
		return fmt.Errorf("SMTP_HOST not set")
	}
	smtpPortStr := os.Getenv("SMTP_PORT")
	if smtpPortStr == "" {
		return fmt.Errorf("SMTP_PORT not set")
	}
	smtpPort, err := strconv.Atoi(smtpPortStr)
	if err != nil {
		return fmt.Errorf("invalid SMTP_PORT: %w", err)
	}
	smtpPass := os.Getenv("SMTP_PASS")
	if smtpPass == "" {
		return fmt.Errorf("SMTP_PASS not set")
	}

	userAgents := typesPkg.Agents{
		Bot:    "headlines_bot/1.0 (+https://github.com/vctrla; " + email + ")",
		Chrome: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36",
		Reader: "RSSReader/1.0 (+https://github.com/vctrla; " + email + ")",
	}

	results := make([]feedResult, len(feeds.Feeds))
	var wg sync.WaitGroup

	for idx, cfg := range feeds.Feeds {
		wg.Add(1)
		go func(i int, fc feeds.FeedConfig) {
			defer wg.Done()

			articles, err := tools.ParseRSSFeed(ctx, userAgents, fc)
			if err != nil {
				logger.Error("Error parsing RSS feed",
					zap.String("url", fc.URL),
					zap.Error(err),
				)
				results[i].Err = err
				return
			}

			toPub, err := collectUnpublished(ctx, articles, db, envTarget)
			if err != nil {
				logger.Error("Error collecting unpublished articles",
					zap.String("source", fc.Header),
					zap.Error(err),
				)
				results[i].Err = err
				return
			}

			results[i].Articles = toPub
		}(idx, cfg)
	}

	wg.Wait()

	// Aggregate results preserving feed order
	allToPublish := make([]typesPkg.MainStruct, 0, 64)
	seen := make(map[string]bool, 256)

	for _, res := range results {
		if res.Err != nil {
			continue
		}
		for _, art := range res.Articles {
			if seen[art.GUID] {
				continue
			}
			seen[art.GUID] = true
			allToPublish = append(allToPublish, art)
		}
	}

	// Nothing new -> done
	if len(allToPublish) == 0 {
		return nil
	}

	loc, _ := time.LoadLocation("Europe/Madrid")
	now := time.Now().In(loc)
	subject := fmt.Sprintf("🫖 Headlines (%d) %dh", len(allToPublish), now.Hour())

	var htmlBody string
	if envTarget == "email" {
		items := make([]string, 0, len(allToPublish))

		for _, art := range allToPublish {
			items = append(items, art.Title) // Title holds the HTML snippet
		}
		htmlBody = buildEmailHTML(items, subject)
	}

	const maxRetries = 2
	var sendErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if envTarget == "email" {
			sendErr = target.SendToEmail(
				ctx,
				email,
				subject,
				smtpHost, smtpPort, smtpPass,
				htmlBody,
			)
		} else {
			sendErr = target.SendToTelegram(allToPublish, telegramBot, telegramChannel)
		}
		if sendErr == nil {
			break
		}
		logger.Warn("SendToEmail failed, will retry",
			zap.Int("attempt", attempt),
			zap.Error(sendErr),
		)
		// small back‑off before next try
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}

	if sendErr != nil {
		logger.Error("Error sending messages",
			zap.Error(sendErr),
		)
		return sendErr
	}

	// Mark published
	if err := dynamo.BatchMarkPublished(ctx, db, allToPublish); err != nil {
		logger.Error("BatchMarkPublished failed after send",
			zap.Int("count", len(allToPublish)), zap.Error(err),
		)
		return err
	}

	logger.Info("Run complete", zap.Int("new_articles", len(allToPublish)))

	return nil
}

func logic(ctx context.Context) error {
	if os.Getenv("TARGET") == "email" && !shouldRunNow() {
		logger.Info("Skipping run: not in allowed hours for email target")
		return nil
	}

	sdkConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("unable to load SDK config: %v", err)
	}

	db := dynamodb.NewFromConfig(sdkConfig)

	return runParsers(ctx, db)
}

func main() {
	ctx := context.Background()
	defer logger.Sync()

	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		// Running in Lambda
		lambda.Start(func(ctx context.Context) error {
			return logic(ctx)
		})
	} else {
		// Running locally
		if err := godotenv.Load(); err != nil {
			logger.Warn("Failed to load .env file",
				zap.Error(err),
				zap.String("note", "This is expected in some environments"),
			)
		}

		if err := logic(ctx); err != nil {
			logger.Fatal("Application failed",
				zap.Error(err),
			)
		}
	}
}

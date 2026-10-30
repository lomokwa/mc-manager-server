package mail

import (
	"fmt"
	"os"
	"time"

	"github.com/resend/resend-go/v4"
)

func SendPasswordReset(to, link string, expiresAt time.Time) error {
	client := resend.NewClient(os.Getenv("RESEND_API_KEY"))

	params := &resend.SendEmailRequest{
		From:    os.Getenv("MAIL_FROM"),
		To:      []string{to},
		Subject: "Reset your password",
		Text: fmt.Sprintf(
			"We received a request to reset your password.\n\n"+
				"Click the link below to choose a new one (valid for 15 minutes):\n\n%s\n\n"+
				"If you didn't request this, you can ignore this email.\n",
			link,
		),
	}

	_, err := client.Emails.Send(params)
	return err
}

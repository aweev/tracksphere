package notify

import (
	"fmt"
	"strings"
)

// BrandConfig holds tenant branding for notification templates
type BrandConfig struct {
	CompanyName  string
	PrimaryColor string
	LogoURL      string
	SupportEmail string
	CustomDomain string
}

// TemplateEngine renders branded notifications
type TemplateEngine struct {
	brand BrandConfig
}

// NewTemplateEngine creates a template engine with tenant branding
func NewTemplateEngine(brand BrandConfig) *TemplateEngine {
	return &TemplateEngine{brand: brand}
}

// RenderEmail renders a branded email template
func (t *TemplateEngine) RenderEmail(subject, body, trackingNumber, carrier, mode, origin, destination, status string, eta *string) string {
	color := t.brand.PrimaryColor
	if color == "" {
		color = "#ff6b00"
	}
	
	logo := ""
	if t.brand.LogoURL != "" {
		logo = fmt.Sprintf(`<img src="%s" alt="%s" style="max-height: 40px; margin-bottom: 16px;">`, t.brand.LogoURL, t.brand.CompanyName)
	}
	
	companyName := t.brand.CompanyName
	if companyName == "" {
		companyName = "TrackSphere"
	}
	
	supportEmail := t.brand.SupportEmail
	if supportEmail == "" {
		supportEmail = "support@tracksphere.io"
	}
	
	// Build ETA line
	etaLine := ""
	if eta != nil && *eta != "" {
		etaLine = fmt.Sprintf(`<tr><td style="padding: 8px 0; color: #64748b;">Estimated Arrival:</td><td style="padding: 8px 0; font-family: monospace; font-weight: 600; color: #0f172a;">%s</td></tr>`, *eta)
	}
	
	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>%s</title>
</head>
<body style="margin: 0; padding: 0; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; background-color: #f8fafc;">
  <div style="max-width: 600px; margin: 0 auto; padding: 24px;">
    <!-- Header -->
    <div style="background: %s; border-radius: 12px 12px 0 0; padding: 24px; text-align: center;">
      %s
      <h1 style="margin: 8px 0 0; color: white; font-size: 24px; font-weight: 700;">%s</h1>
    </div>
    
    <!-- Content -->
    <div style="background: white; border-radius: 0 0 12px 12px; padding: 32px; box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.1);">
      <h2 style="margin: 0 0 16px; color: #0f172a; font-size: 20px;">%s</h2>
      
      <p style="margin: 0 0 24px; color: #475569; line-height: 1.6;">%s</p>
      
      <!-- Shipment Details Table -->
      <table style="width: 100%%; border-collapse: collapse; margin-bottom: 24px;">
        <tr>
          <td style="padding: 8px 0; color: #64748b; width: 40%%;">Tracking Number:</td>
          <td style="padding: 8px 0; font-family: monospace; font-weight: 600; color: #0f172a;">%s</td>
        </tr>
        <tr>
          <td style="padding: 8px 0; color: #64748b;">Carrier:</td>
          <td style="padding: 8px 0; font-weight: 500; color: #0f172a;">%s</td>
        </tr>
        <tr>
          <td style="padding: 8px 0; color: #64748b;">Mode:</td>
          <td style="padding: 8px 0; text-transform: capitalize; font-weight: 500; color: #0f172a;">%s</td>
        </tr>
        <tr>
          <td style="padding: 8px 0; color: #64748b;">Route:</td>
          <td style="padding: 8px 0; font-weight: 500; color: #0f172a;">%s → %s</td>
        </tr>
        <tr>
          <td style="padding: 8px 0; color: #64748b;">Status:</td>
          <td style="padding: 8px 0;">
            <span style="display: inline-block; padding: 4px 12px; border-radius: 9999px; font-size: 12px; font-weight: 600; background-color: %s; color: %s;">%s</td>
        </tr>
        %s
      </table>
      
      <hr style="border: none; border-top: 1px solid #e2e8f0; margin: 24px 0;">
      
      <p style="margin: 0; color: #64748b; font-size: 14px;">
        This notification was sent by <strong>%s</strong>. 
        <a href="mailto:%s" style="color: %s;">%s</a>
      </p>
    </div>
    
    <!-- Footer -->
    <div style="text-align: center; margin-top: 16px; padding: 16px; color: #94a3b8; font-size: 12px;">
      © 2024 %s. All rights reserved.
    </div>
  </div>
</body>
</html>
`, subject, color, logo, companyName, subject, body, trackingNumber, carrier, mode, origin, destination, 
   statusBackground(status), statusText(status), strings.Title(status), etaLine,
   companyName, supportEmail, color, supportEmail, companyName)
}

// RenderSMS renders a branded SMS template
func (t *TemplateEngine) RenderSMS(subject, body, trackingNumber, carrier, status string) string {
	companyName := t.brand.CompanyName
	if companyName == "" {
		companyName = "TrackSphere"
	}
	return fmt.Sprintf("[%s] %s: %s (%s) - %s", companyName, subject, trackingNumber, carrier, status)
}

// RenderWhatsApp renders a branded WhatsApp template
func (t *TemplateEngine) RenderWhatsApp(subject, body, trackingNumber, carrier, origin, destination, status string, eta *string) string {
	companyName := t.brand.CompanyName
	if companyName == "" {
		companyName = "TrackSphere"
	}
	
	etaLine := ""
	if eta != nil && *eta != "" {
		etaLine = fmt.Sprintf("\nETA: %s", *eta)
	}
	
	return fmt.Sprintf(`*%s Update*
%s

📦 *Tracking:* %s
🚢 *Carrier:* %s
📍 *Route:* %s → %s
📊 *Status:* %s%s

Need help? Reply HELP or visit %s`, companyName, subject, trackingNumber, carrier, origin, destination, status, etaLine, t.brand.SupportEmail)
}

func statusBackground(status string) string {
	switch strings.ToLower(status) {
	case "booked":
		return "#e2e8f0"
	case "in_transit":
		return "#dbeafe"
	case "at_customs":
		return "#fef3c7"
	case "out_for_delivery":
		return "#ede9fe"
	case "delivered":
		return "#dcfce7"
	case "exception":
		return "#fee2e2"
	case "cancelled":
		return "#e2e8f0"
	default:
		return "#e2e8f0"
	}
}

func statusText(status string) string {
	switch strings.ToLower(status) {
	case "booked":
		return "#334155"
	case "in_transit":
		return "#1d4ed8"
	case "at_customs":
		return "#b45309"
	case "out_for_delivery":
		return "#7c3aed"
	case "delivered":
		return "#16a34a"
	case "exception":
		return "#dc2626"
	case "cancelled":
		return "#475569"
	default:
		return "#334155"
	}
}
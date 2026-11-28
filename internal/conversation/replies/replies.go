package replies

import (
	"fmt"
	"strings"
)

// Language identifies a reply language.
type Language string

const (
	LangHindi   Language = "hi"
	LangEnglish Language = "en"
)

// FromUserLanguage maps the users.language column to a reply language.
func FromUserLanguage(lang string) Language {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "hi", "hindi", "hinglish":
		return LangHindi
	case "en", "english":
		return LangEnglish
	default:
		return LangHindi
	}
}

// Reply is a rendered message.
type Reply struct {
	Text string
}

// Templates holds all reply strings for one language.
type Templates struct {
	AskPickup           string
	AskDrop             string
	Confirm             string
	Booked              string
	Cancelled           string
	Unknown             string
	Help                string
	ClarifyPlace        string
	TooManyAttempts     string
	Timeout             string
	BookingFailed       string
	NoPendingBooking    string
	AlreadyBooked       string
	BookingInProgress   string
	CancelDuringBooking string
	CancelDuringRide    string
	BookingBeingChecked string
}

// For returns the template set for a language.
func For(lang Language) Templates {
	switch lang {
	case LangEnglish:
		return englishTemplates
	default:
		return hindiTemplates
	}
}

// RenderAskPickup returns the "where from?" prompt.
func RenderAskPickup(lang Language) string {
	return For(lang).AskPickup
}

// RenderAskDrop returns the "where to?" prompt.
func RenderAskDrop(lang Language) string {
	return For(lang).AskDrop
}

// RenderConfirm builds the confirmation message.
func RenderConfirm(lang Language, pickup, drop string, farePaise int64) string {
	tmpl := For(lang).Confirm
	if farePaise <= 0 {
		// Omit the fare line when no fare is known. Showing "Rs. 0.00"
		// is misleading.
		tmpl = strings.ReplaceAll(tmpl, "Estimated fare: Rs. {fare}\n", "Fare will appear in the cab app.\n")
	}
	t := tmpl
	t = strings.ReplaceAll(t, "{pickup}", pickup)
	t = strings.ReplaceAll(t, "{drop}", drop)
	t = strings.ReplaceAll(t, "{fare}", FormatFare(farePaise))
	return t
}

// RenderBooked builds the "booked" message.
func RenderBooked(lang Language, deepLink string) string {
	t := For(lang).Booked
	t = strings.ReplaceAll(t, "{link}", deepLink)
	return t
}

// RenderCancelled returns the cancellation message.
func RenderCancelled(lang Language) string {
	return For(lang).Cancelled
}

// RenderUnknown returns the "didn't understand" message.
func RenderUnknown(lang Language) string {
	return For(lang).Unknown
}

// RenderHelp returns the help message.
func RenderHelp(lang Language) string {
	return For(lang).Help
}

// RenderClarifyPlace asks which place the user meant.
func RenderClarifyPlace(lang Language, place string) string {
	t := For(lang).ClarifyPlace
	t = strings.ReplaceAll(t, "{place}", place)
	return t
}

// RenderTooManyAttempts tells the user we're giving up and offering to notify.
func RenderTooManyAttempts(lang Language) string {
	return For(lang).TooManyAttempts
}

// RenderTimeout returns the timeout message.
func RenderTimeout(lang Language) string {
	return For(lang).Timeout
}

// RenderBookingFailed returns the booking failure message.
func RenderBookingFailed(lang Language) string {
	return For(lang).BookingFailed
}

// RenderNoPendingBooking returns the "nothing to confirm" message.
func RenderNoPendingBooking(lang Language) string {
	return For(lang).NoPendingBooking
}

// RenderSafetyDeny returns a deny message for a safety reason.
func RenderSafetyDeny(lang Language, reason string) string {
	switch reason {
	case "kill_switch":
		if lang == LangEnglish {
			return "Bookings are currently disabled by your family."
		}
		return "Aapke family ne bookings band kar di hain."
	case "per_ride_cap":
		if lang == LangEnglish {
			return "This ride exceeds your per-ride spending limit."
		}
		return "Yeh ride aapki per-ride limit se zyada hai."
	case "daily_cap":
		if lang == LangEnglish {
			return "You have reached your daily spending limit."
		}
		return "Aapki daily spending limit puri ho gayi hai."
	default:
		if lang == LangEnglish {
			return "This booking is not allowed right now."
		}
		return "Yeh booking abhi allowed nahi hai."
	}
}

// RenderApprovalNeeded returns the "being checked" message.
func RenderApprovalNeeded(lang Language) string {
	if lang == LangEnglish {
		return "Let me check with your family first. I'll get back to you."
	}
	return "Main aapke family se pooch raha hoon. Thoda wait karo."
}

// RenderAlreadyBooked returns the "already booked" message.
func RenderAlreadyBooked(lang Language) string {
	return For(lang).AlreadyBooked
}

// RenderBookingInProgress returns the "booking in progress" message.
func RenderBookingInProgress(lang Language) string {
	return For(lang).BookingInProgress
}

// RenderCancelDuringBooking returns the "already being booked" message.
func RenderCancelDuringBooking(lang Language) string {
	return For(lang).CancelDuringBooking
}

// RenderCancelDuringRide returns the "ride already booked" message.
func RenderCancelDuringRide(lang Language) string {
	return For(lang).CancelDuringRide
}

// RenderBookingBeingChecked returns the "being checked" message.
func RenderBookingBeingChecked(lang Language) string {
	return For(lang).BookingBeingChecked
}

// FormatFare formats paise as rupees with two decimal places.
func FormatFare(paise int64) string {
	rupees := paise / 100
	p := paise % 100
	if p < 0 {
		p = -p
	}
	return fmt.Sprintf("%d.%02d", rupees, p)
}

var hindiTemplates = Templates{
	AskPickup:           "Kahan se jaana hai?",
	AskDrop:             "Kahan jaana hai?",
	Confirm:             "Pickup: {pickup}\nDrop: {drop}\nEstimated fare: Rs. {fare}\nKya yeh sahi hai? Haan ya Nahi?",
	Booked:              "Cab book ho gaya! Link: {link}",
	Cancelled:           "Theek hai, cancel kar diya.",
	Unknown:             "Samajh nahi aaya. Dobara boliye.",
	Help:                "Main cab book kar sakta hoon. Boliye 'cab book karo' aur main madad karunga.",
	ClarifyPlace:        "{place} kaunsa? Kripya poora address bataiye.",
	TooManyAttempts:     "Main samajh nahi pa raha. Main aapke bete ko bata deta hoon.",
	Timeout:             "Bahut der ho gayi. Dobara shuru karein.",
	BookingFailed:       "Booking nahi ho payi. Kripya dobara try karein.",
	NoPendingBooking:    "Koi pending booking nahi hai.",
	AlreadyBooked:       "Aapki ride already book hai.",
	BookingInProgress:   "Booking chal rahi hai, ruko.",
	CancelDuringBooking: "Booking chal rahi hai, cancel nahi ho sakta abhi.",
	CancelDuringRide:    "Ride book ho chuki hai, cancel nahi ho sakta.",
	BookingBeingChecked: "Ride check ho rahi hai, thoda wait karo.",
}

var englishTemplates = Templates{
	AskPickup:           "Where should I pick you up from?",
	AskDrop:             "Where do you want to go?",
	Confirm:             "Pickup: {pickup}\nDrop: {drop}\nEstimated fare: Rs. {fare}\nIs this correct? Yes or No?",
	Booked:              "Cab booked! Link: {link}",
	Cancelled:           "Okay, cancelled.",
	Unknown:             "I didn't understand. Please say that again.",
	Help:                "I can book a cab for you. Say 'book a cab' and I'll help.",
	ClarifyPlace:        "Which {place}? Please tell me the address.",
	TooManyAttempts:     "I couldn't understand. I'll let your child know.",
	Timeout:             "It's been a while. Let's start over.",
	BookingFailed:       "Booking failed. Please try again.",
	NoPendingBooking:    "No pending booking.",
	AlreadyBooked:       "Your ride is already booked.",
	BookingInProgress:   "Booking in progress, please wait.",
	CancelDuringBooking: "Booking in progress, cannot cancel now.",
	CancelDuringRide:    "Ride already booked, cannot cancel.",
	BookingBeingChecked: "Ride is being checked, please wait.",
}

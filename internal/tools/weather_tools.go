package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// RegisterWeatherTools registers weather_current and weather_forecast tools.
// Uses Open-Meteo (open-meteo.com) — free, no API key required, no sign-up.
// Geocoding via Open-Meteo's geocoding API.
func RegisterWeatherTools(reg *Registry) {
	reg.Register(&Tool{
		Name: "weather_current",
		Description: `Get current weather conditions and today's forecast for any location.
Returns: temperature, feels-like, humidity, wind, precipitation probability, UV index, and a plain-English summary.
Use this for: "will it rain?", "what's the weather?", "should I bring an umbrella?", "what's the temperature in X?"
No API key required. Works for any city, region, or landmark worldwide.`,
		MinTier: security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["location"],
			"properties": {
				"location": {
					"type": "string",
					"description": "City, region, landmark, or coordinates. Examples: 'Orcas Island WA', 'London', 'Tokyo', '48.7,-122.9'"
				},
				"units": {
					"type": "string",
					"enum": ["imperial", "metric"],
					"description": "Temperature units. Default: imperial (°F). Use metric for °C."
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, params json.RawMessage) (string, error) {
			var input struct {
				Location string `json:"location"`
				Units    string `json:"units"`
			}
			if err := json.Unmarshal(params, &input); err != nil {
				return "", fmt.Errorf("invalid parameters: %w", err)
			}
			if input.Location == "" {
				return "", fmt.Errorf("location is required")
			}
			if input.Units == "" {
				input.Units = "imperial"
			}

			return getWeather(ctx, input.Location, input.Units, false)
		},
	})

	reg.Register(&Tool{
		Name: "weather_forecast",
		Description: `Get a 7-day weather forecast for any location.
Returns: daily high/low temperatures, precipitation probability, conditions, and sunrise/sunset times.
Use this for: "what's the weather like this week?", "will it be sunny on Saturday?", "weekend weather forecast"`,
		MinTier: security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"required": ["location"],
			"properties": {
				"location": {
					"type": "string",
					"description": "City, region, landmark, or coordinates."
				},
				"units": {
					"type": "string",
					"enum": ["imperial", "metric"],
					"description": "Temperature units. Default: imperial (°F)."
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, params json.RawMessage) (string, error) {
			var input struct {
				Location string `json:"location"`
				Units    string `json:"units"`
			}
			if err := json.Unmarshal(params, &input); err != nil {
				return "", fmt.Errorf("invalid parameters: %w", err)
			}
			if input.Location == "" {
				return "", fmt.Errorf("location is required")
			}
			if input.Units == "" {
				input.Units = "imperial"
			}

			return getWeather(ctx, input.Location, input.Units, true)
		},
	})
}

// --- Open-Meteo implementation ---

const (
	openMeteoGeoURL  = "https://geocoding-api.open-meteo.com/v1/search"
	openMeteoAPIURL  = "https://api.open-meteo.com/v1/forecast"
	weatherHTTPTimeout = 10 * time.Second
)

var weatherHTTPClient = &http.Client{Timeout: weatherHTTPTimeout}

type geoResult struct {
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Country   string  `json:"country"`
	Admin1    string  `json:"admin1"` // state/province
}

func geocode(ctx context.Context, location string) (*geoResult, error) {
	// Try parsing as lat,lon first.
	var lat, lon float64
	if n, _ := fmt.Sscanf(location, "%f,%f", &lat, &lon); n == 2 {
		return &geoResult{Name: location, Latitude: lat, Longitude: lon}, nil
	}

	reqURL := openMeteoGeoURL + "?name=" + url.QueryEscape(location) + "&count=1&language=en&format=json"
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := weatherHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("geocoding request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Results []geoResult `json:"results"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("geocoding parse failed: %w", err)
	}
	if len(result.Results) == 0 {
		return nil, fmt.Errorf("location %q not found — try a nearby city or use lat,lon coordinates", location)
	}
	return &result.Results[0], nil
}

func getWeather(ctx context.Context, location, units string, forecast bool) (string, error) {
	loc, err := geocode(ctx, location)
	if err != nil {
		return "", err
	}

	tempUnit := "fahrenheit"
	tempSym := "°F"
	windUnit := "mph"
	if units == "metric" {
		tempUnit = "celsius"
		tempSym = "°C"
		windUnit = "kmh"
	}

	params := url.Values{}
	params.Set("latitude", fmt.Sprintf("%.4f", loc.Latitude))
	params.Set("longitude", fmt.Sprintf("%.4f", loc.Longitude))
	params.Set("temperature_unit", tempUnit)
	params.Set("wind_speed_unit", windUnit)
	params.Set("precipitation_unit", "inch")
	params.Set("timezone", "auto")
	params.Set("current", strings.Join([]string{
		"temperature_2m", "apparent_temperature", "relative_humidity_2m",
		"precipitation", "precipitation_probability", "weather_code",
		"wind_speed_10m", "wind_direction_10m", "wind_gusts_10m",
		"cloud_cover", "uv_index", "is_day",
	}, ","))

	forecastDays := "1"
	if forecast {
		forecastDays = "7"
	}
	params.Set("forecast_days", forecastDays)
	params.Set("daily", strings.Join([]string{
		"weather_code", "temperature_2m_max", "temperature_2m_min",
		"apparent_temperature_max", "apparent_temperature_min",
		"sunrise", "sunset",
		"precipitation_sum", "precipitation_probability_max",
		"wind_speed_10m_max", "wind_gusts_10m_max",
	}, ","))

	reqURL := openMeteoAPIURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := weatherHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("weather request failed: %w", err)
	}
	defer resp.Body.Close()

	var data struct {
		Timezone string `json:"timezone"`
		Current  struct {
			Time                  string  `json:"time"`
			Temperature2m         float64 `json:"temperature_2m"`
			ApparentTemperature   float64 `json:"apparent_temperature"`
			RelativeHumidity2m    int     `json:"relative_humidity_2m"`
			Precipitation         float64 `json:"precipitation"`
			PrecipitationProb     int     `json:"precipitation_probability"`
			WeatherCode           int     `json:"weather_code"`
			WindSpeed10m          float64 `json:"wind_speed_10m"`
			WindDirection10m      int     `json:"wind_direction_10m"`
			WindGusts10m          float64 `json:"wind_gusts_10m"`
			CloudCover            int     `json:"cloud_cover"`
			UVIndex               float64 `json:"uv_index"`
			IsDay                 int     `json:"is_day"`
		} `json:"current"`
		Daily struct {
			Time                    []string  `json:"time"`
			WeatherCode             []int     `json:"weather_code"`
			Temperature2mMax        []float64 `json:"temperature_2m_max"`
			Temperature2mMin        []float64 `json:"temperature_2m_min"`
			ApparentTemperatureMax  []float64 `json:"apparent_temperature_max"`
			ApparentTemperatureMin  []float64 `json:"apparent_temperature_min"`
			Sunrise                 []string  `json:"sunrise"`
			Sunset                  []string  `json:"sunset"`
			PrecipitationSum        []float64 `json:"precipitation_sum"`
			PrecipitationProbMax    []int     `json:"precipitation_probability_max"`
			WindSpeed10mMax         []float64 `json:"wind_speed_10m_max"`
			WindGusts10mMax         []float64 `json:"wind_gusts_10m_max"`
		} `json:"daily"`
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("weather parse failed: %w", err)
	}

	// Build display name.
	displayName := loc.Name
	if loc.Admin1 != "" {
		displayName += ", " + loc.Admin1
	}
	if loc.Country != "" && loc.Country != "United States" {
		displayName += ", " + loc.Country
	}

	var sb strings.Builder

	// Current conditions.
	c := data.Current
	sb.WriteString(fmt.Sprintf("## Weather for %s\n\n", displayName))
	sb.WriteString(fmt.Sprintf("**%s** — %s\n\n",
		weatherDescription(c.WeatherCode), dayNight(c.IsDay)))
	sb.WriteString(fmt.Sprintf("🌡️  **%.0f%s** (feels like %.0f%s)\n",
		c.Temperature2m, tempSym, c.ApparentTemperature, tempSym))
	sb.WriteString(fmt.Sprintf("🌧️  Precipitation chance: **%d%%**", c.PrecipitationProb))
	if c.Precipitation > 0 {
		sb.WriteString(fmt.Sprintf(" (%.2f\" falling now)", c.Precipitation))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("💨  Wind: **%.0f %s** from %s",
		c.WindSpeed10m, windUnit, compassDirection(c.WindDirection10m)))
	if c.WindGusts10m > c.WindSpeed10m*1.3 {
		sb.WriteString(fmt.Sprintf(" (gusts to %.0f %s)", c.WindGusts10m, windUnit))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("💧  Humidity: %d%%\n", c.RelativeHumidity2m))
	sb.WriteString(fmt.Sprintf("☁️  Cloud cover: %d%%\n", c.CloudCover))
	if c.UVIndex > 0 {
		sb.WriteString(fmt.Sprintf("☀️  UV Index: %.0f (%s)\n", c.UVIndex, uvDescription(c.UVIndex)))
	}

	// Today's summary from daily.
	if len(data.Daily.Time) > 0 {
		sb.WriteString(fmt.Sprintf("\n📅 **Today:** High %.0f%s / Low %.0f%s",
			data.Daily.Temperature2mMax[0], tempSym,
			data.Daily.Temperature2mMin[0], tempSym))
		if len(data.Daily.PrecipitationProbMax) > 0 {
			sb.WriteString(fmt.Sprintf(" • Rain chance: %d%%", data.Daily.PrecipitationProbMax[0]))
		}
		if len(data.Daily.Sunrise) > 0 {
			sunrise := formatTime(data.Daily.Sunrise[0])
			sunset := formatTime(data.Daily.Sunset[0])
			sb.WriteString(fmt.Sprintf(" • Sunrise %s, Sunset %s", sunrise, sunset))
		}
		sb.WriteString("\n")
	}

	if !forecast || len(data.Daily.Time) <= 1 {
		return sb.String(), nil
	}

	// 7-day forecast.
	sb.WriteString("\n### 7-Day Forecast\n\n")
	for i := 1; i < len(data.Daily.Time) && i < 7; i++ {
		dayLabel := formatDayLabel(data.Daily.Time[i])
		rainChance := 0
		if i < len(data.Daily.PrecipitationProbMax) {
			rainChance = data.Daily.PrecipitationProbMax[i]
		}
		hi := data.Daily.Temperature2mMax[i]
		lo := data.Daily.Temperature2mMin[i]
		desc := weatherDescription(data.Daily.WeatherCode[i])
		rain := ""
		if i < len(data.Daily.PrecipitationSum) && data.Daily.PrecipitationSum[i] > 0.01 {
			rain = fmt.Sprintf(" • %.2f\" rain", data.Daily.PrecipitationSum[i])
		}
		sb.WriteString(fmt.Sprintf("**%s** — %s  H:%.0f%s L:%.0f%s  🌧️ %d%%%s\n",
			dayLabel, desc, hi, tempSym, lo, tempSym, rainChance, rain))
	}

	sb.WriteString(fmt.Sprintf("\n*Data from Open-Meteo.com • Coordinates: %.4f, %.4f*",
		loc.Latitude, loc.Longitude))

	return sb.String(), nil
}

// --- WMO weather code descriptions ---

func weatherDescription(code int) string {
	switch {
	case code == 0:
		return "Clear sky ☀️"
	case code == 1:
		return "Mainly clear 🌤️"
	case code == 2:
		return "Partly cloudy ⛅"
	case code == 3:
		return "Overcast ☁️"
	case code >= 45 && code <= 48:
		return "Foggy 🌫️"
	case code >= 51 && code <= 53:
		return "Light drizzle 🌦️"
	case code >= 55 && code <= 57:
		return "Heavy drizzle 🌧️"
	case code >= 61 && code <= 63:
		return "Light rain 🌧️"
	case code == 65:
		return "Heavy rain 🌧️"
	case code >= 66 && code <= 67:
		return "Freezing rain 🌨️"
	case code >= 71 && code <= 73:
		return "Light snow 🌨️"
	case code == 75:
		return "Heavy snow ❄️"
	case code == 77:
		return "Snow grains ❄️"
	case code >= 80 && code <= 82:
		return "Rain showers 🌦️"
	case code == 85 || code == 86:
		return "Snow showers 🌨️"
	case code == 95:
		return "Thunderstorm ⛈️"
	case code >= 96 && code <= 99:
		return "Thunderstorm with hail ⛈️"
	default:
		return "Unknown conditions"
	}
}

func compassDirection(deg int) string {
	dirs := []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}
	idx := int(math.Round(float64(deg)/45)) % 8
	return dirs[idx]
}

func uvDescription(uv float64) string {
	switch {
	case uv < 3:
		return "Low"
	case uv < 6:
		return "Moderate"
	case uv < 8:
		return "High"
	case uv < 11:
		return "Very High"
	default:
		return "Extreme"
	}
}

func dayNight(isDay int) string {
	if isDay == 1 {
		return "Daytime"
	}
	return "Nighttime"
}

func formatTime(iso string) string {
	// ISO format from open-meteo: "2026-03-26T06:42"
	parts := strings.Split(iso, "T")
	if len(parts) < 2 {
		return iso
	}
	t, err := time.Parse("15:04", parts[1])
	if err != nil {
		return parts[1]
	}
	return t.Format("3:04 PM")
}

func formatDayLabel(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("Mon Jan 2")
}

package main

import (
	"reflect"
	"testing"
)

func TestParseGeminiResponse(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		want    []ReceiptData
		wantErr bool
	}{
		{
			name:    "single object",
			jsonStr: `{"date": "2023-10-27", "vendor": "Test Clinic", "category": "Medical", "total_amount": 1500}`,
			want: []ReceiptData{
				{Date: "2023-10-27", Vendor: "Test Clinic", Category: "Medical", Amount: 1500},
			},
			wantErr: false,
		},
		{
			name:    "array of objects",
			jsonStr: `[{"date": "2023-10-27", "vendor": "Test Clinic", "category": "Medical", "total_amount": 1500}, {"date": "2023-10-28", "vendor": "Grocery Store", "category": "Grocery", "total_amount": 2500}]`,
			want: []ReceiptData{
				{Date: "2023-10-27", Vendor: "Test Clinic", Category: "Medical", Amount: 1500},
				{Date: "2023-10-28", Vendor: "Grocery Store", Category: "Grocery", Amount: 2500},
			},
			wantErr: false,
		},
		{
			name:    "markdown wrapped json",
			jsonStr: "```json\n" + `{"date": "2023-10-27", "vendor": "Test Clinic", "category": "Medical", "total_amount": 1500}` + "\n```",
			want: []ReceiptData{
				{Date: "2023-10-27", Vendor: "Test Clinic", Category: "Medical", Amount: 1500},
			},
			wantErr: false,
		},
		{
			name:    "invalid json",
			jsonStr: `{"date": "2023-10-27"`, // missing closing brace
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGeminiResponse(tt.jsonStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseGeminiResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseGeminiResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}

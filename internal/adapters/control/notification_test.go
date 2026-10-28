package control

import "testing"

func TestNotificationControlStrictReadOnlyRequests(t *testing.T) {
	for _, test := range []struct {
		request string
		valid   bool
	}{
		{`{"version":1,"op":"notifications","subscription_id":"11111111-1111-4111-8111-111111111111"}`, true},
		{`{"version":1,"op":"notification","notification_id":"11111111-1111-4111-8111-111111111111"}`, true},
		{`{"version":1,"op":"notification","notification_id":"11111111-1111-4111-8111-111111111111","offset":1}`, false},
		{`{"version":1,"op":"notification","notification_id":"11111111-1111-4111-8111-111111111111","revision":"no"}`, false},
		{`{"version":1,"op":"notifications","subscription_id":"11111111-1111-4111-8111-111111111111","limit":21}`, false},
		{`{"version":1,"op":"notifications","subscription_id":"11111111-1111-4111-8111-111111111111","limit":1.0}`, false},
		{`{"version":1,"op":"notifications","subscription_id":"11111111-1111-4111-8111-111111111111","after":null}`, false},
		{`{"version":1,"op":"notification","notification_id":"00000000-0000-0000-0000-000000000000"}`, false},
		{`{"version":1,"op":"notification","notification_id":"11111111-1111-4111-8111-111111111111","retry":true}`, false},
		{`{"version":1,"op":"retry","notification_id":"11111111-1111-4111-8111-111111111111"}`, false},
	} {
		_, err := parseRequest([]byte(test.request))
		if (err == nil) != test.valid {
			t.Fatalf("request=%s valid=%v error=%v", test.request, test.valid, err)
		}
	}
}

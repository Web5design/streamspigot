package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/araddon/httpstream"
)

const followingListUpdateIntervalNanosec = 1 * 60 * 60 * 1e9 // 1 hour

var twitterUsername *string = flag.String("twitter_username", "", "Twitter account username to use to connect to the Streaming API")
var twitterPassword *string = flag.String("twitter_password", "", "Password for the Twitter account")
var streamSpigotHostname *string = flag.String("stream_spigot_hostname", "", "Host where Stream Spigot is running")
var streamSpigotSecret *string = flag.String("stream_spigot_secret", "", "Secret key that must be passed in all Stream Spigot HTTP requests")

func main() {
	flag.Parse()

	if len(*twitterUsername) == 0 || len(*twitterPassword) == 0 || len(*streamSpigotHostname) == 0 || len(*streamSpigotSecret) == 0 {
		flag.Usage()
		return
	}

	baseUrl := fmt.Sprintf("http://%s/bird-feeder/pinger/", *streamSpigotHostname)
	followingUrl := fmt.Sprintf("%sfollowing?secret=%s", baseUrl, url.QueryEscape(*streamSpigotSecret))
	pingUrl := baseUrl + "ping"

	var followingUserIds []int64
	var followingUserIdMap map[int64]bool

	stream := make(chan []byte)
	done := make(chan bool)
	updateFollowingListTick := time.Tick(followingListUpdateIntervalNanosec)

	client := httpstream.NewBasicAuthClient(*twitterUsername, *twitterPassword, func(line []byte) {
		stream <- line
	})

	updateFollowingList := func() {
		followingUserIds, followingUserIdMap = getFollowingList(followingUrl)

		fmt.Printf("Tracking updates for %d users...\n", len(followingUserIds))

		client.Close()
		err := client.Filter(followingUserIds, nil, false, done)
		if err != nil {
			fmt.Println(err)
		}
	}

	updateFollowingList()

	for {
		select {
		case <-updateFollowingListTick:
			updateFollowingList()
		case <-done:
			fmt.Printf("Client says it's done")
		case line := <-stream:
			switch {
			case bytes.HasPrefix(line, []byte(`{"event":`)):
				var event httpstream.Event
				json.Unmarshal(line, &event)
			case bytes.HasPrefix(line, []byte(`{"friends":`)):
				var friends httpstream.FriendList
				json.Unmarshal(line, &friends)
			default:
				tweet := httpstream.Tweet{}
				json.Unmarshal(line, &tweet)
				if tweet.User != nil && tweet.User.Id != nil {
					fmt.Printf("%s: %s\n", tweet.User.ScreenName, tweet.Text)

					userId := int64(*tweet.User.Id)

					// We ignore tweets that come from users that we're not following (the
					// Streaming API will also notify when tweets of theirs are retweeted or
					// replied to).
					if _, inMap := followingUserIdMap[userId]; inMap {
						// Similarly, we ignore tweets that are in reply to users that aren't
						// being followed. This will have false negatives: if user A follows X
						// and user B follows X and Z, a reply by X to Z will cause both A and
						// B's streams to get pinged, even though A won't actually see that
						// status. However, that should be rare.
						if (tweet.In_reply_to_user_id != nil) {
							if in_reply_to_user_id := int64(*tweet.In_reply_to_user_id); in_reply_to_user_id != 0 {
								if _, inMap := followingUserIdMap[in_reply_to_user_id]; !inMap {
									continue
								}
							}
						}

						go pingUser(userId, int64(*tweet.Id), pingUrl)
					}
				} else {
					fmt.Printf("No tweet?")
				}
			}
		}
	}
}

func getFollowingList(followingUrl string) (followingUserIds []int64, followingUserIdMap map[int64]bool) {
	resp, getErr := http.Get(followingUrl)
	if getErr != nil {
		fmt.Printf("Got error %s when trying to fetch following list\n", getErr)
		os.Exit(1)
	}

	if resp.StatusCode != 200 {
		fmt.Printf("  ...got HTTP status %d when trying to fetch following list\n", resp.StatusCode)
		return
	}

	contents, readErr := ioutil.ReadAll(resp.Body)
	if readErr != nil {
		resp.Body.Close()
		fmt.Printf("Got error %s when trying to read following list\n", readErr)
		os.Exit(1)
	}
	resp.Body.Close()

	jsonErr := json.Unmarshal(contents, &followingUserIds)
	if jsonErr != nil {
		fmt.Printf("Got error %s when trying to decode JSON\n", jsonErr)
		os.Exit(1)
	}

	followingUserIdMap = make(map[int64]bool)
	for _, v := range followingUserIds {
		followingUserIdMap[v] = true
	}
	return
}

func pingUser(twitterId int64, statusId int64, pingUrl string) {
	fmt.Printf("Pinging for update %d by user %d...\n", statusId, twitterId)

	resp, postErr := http.PostForm(
		pingUrl,
		url.Values{
			"update_twitter_id": {fmt.Sprintf("%d", twitterId)},
			"update_status_id":  {fmt.Sprintf("%d", statusId)},
			"secret":            {*streamSpigotSecret},
		})
	if postErr != nil {
		fmt.Printf("   ...got error %s when trying to POST ping\n", postErr)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Printf("  ...got HTTP status %d when trying to POST ping\n", resp.StatusCode)
		return
	}

	fmt.Printf("   ...success\n")
}

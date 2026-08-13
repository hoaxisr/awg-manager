package wdtt

import "testing"

func TestDecodeImport_WdttConnectQuery(t *testing.T) {
	link := "wdtt://connect?v=1&host=45.128.235.176&dtls=56000&wg=56001&local=9000&password=pRujBTrEnykvnG23&hashes=focqLvg-E1od3rNdx0Q0NGDpNXtf0NF5QU-CCu6PHPQ%2CjV8BmeP_zNwNfDT46nIgqw9yGZ-5Vn3y0XbnJ158TR0%2Ck_hR9nkEb7zNCtehd1H52KdQdyOjrvEz-9ifiBeyH04%2C6ODHK91tF6W34-7op-GvePwdUGWZFRuX3S8IBcrZUIw"
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "45.128.235.176:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
	if got.Password != "pRujBTrEnykvnG23" {
		t.Fatalf("password=%q", got.Password)
	}
	if got.Listen != "127.0.0.1:9000" {
		t.Fatalf("listen=%q", got.Listen)
	}
	if len(got.VKHashes) != 4 {
		t.Fatalf("hashes=%v", got.VKHashes)
	}
	if got.VKHashes[0] != "focqLvg-E1od3rNdx0Q0NGDpNXtf0NF5QU-CCu6PHPQ" {
		t.Fatalf("hash0=%q", got.VKHashes[0])
	}
}

func TestDecodeImport_WdttConnectQueryWithName(t *testing.T) {
	link := "wdtt://connect?host=10.0.0.1&dtls=56000&password=secret&hashes=h1#MyPlus"
	got, err := DecodeImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "MyPlus" {
		t.Fatalf("name=%q", got.Name)
	}
	if got.Peer != "10.0.0.1:56000" {
		t.Fatalf("peer=%q", got.Peer)
	}
}

package main

import (
	"context"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"log"
	"time"
)

const uri = "mongodb://192.168.2.110:8000"

// ConnectToMongo Connect TODO 需要设置连接超时和读取超时
func ConnectToMongo() (*mongo.Collection, *mongo.Client) {
	// Create a new client and connect to the server
	defer func() {
		if err := recover(); err != nil {
			log.Println("[ERROR ConnectToMongo]:", err)
		}
	}()
	client, err := mongo.Connect(context.TODO(), options.Client().ApplyURI(uri).SetConnectTimeout(5*time.Second))
	if err != nil {
		panic(err)
	}

	//defer func() {
	//	if err = client.Disconnect(context.TODO()); err != nil {
	//		panic(err)
	//	}
	//}()

	// Ping the primary
	if err := client.Ping(context.TODO(), readpref.Primary()); err != nil {
		panic(err)
	}
	log.Println("Successfully connected and pinged.")
	return client.Database("test").Collection("runoob"), client
}

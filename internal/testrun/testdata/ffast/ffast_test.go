package ffast

import "testing"

func TestA(t *testing.T) { t.Error("first fails") }
func TestB(t *testing.T) {}
func TestC(t *testing.T) {}

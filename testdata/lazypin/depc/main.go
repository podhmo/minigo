package depc

var Touched = boom()

func boom() int { panic("BOOM: depc initialized") }

func Get() int { return Touched }

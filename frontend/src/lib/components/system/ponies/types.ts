export interface TicketOrder {
	passengerName: string;
	destination: string;
	serviceClass: string;
	options: string[];
	ticketNumber: string;
	seat: string;
	flightTime: string;
	priceGlitter: number;
}

export interface PonyDestination {
	id: string;
	name: string;
	desc: string;
}

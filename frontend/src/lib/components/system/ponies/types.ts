export type PonyClass = 'vip' | 'business' | 'eco';
export type PonyOption = 'vpn' | 'marshmallow' | 'rainbow';

/**
 * Заказ хранит только коды: подписи (класс, опции, направление, имя по умолчанию)
 * вычисляются при отрисовке, чтобы билет следовал за языком интерфейса.
 */
export interface TicketOrder {
	/** Введённое имя без пробелов по краям; null — поле не трогали (имя по умолчанию). */
	passengerName: string | null;
	destinationId: string;
	serviceClass: PonyClass;
	options: PonyOption[];
	ticketNumber: string;
	seat: string;
	priceGlitter: number;
}

export interface PonyDestination {
	id: string;
	name: string;
	desc: string;
}
